// Package egovhttp is the read-only HTTP-GET boundary to the e-Gov 法令 API v2.
// It enforces the DISCIPLINE §1 obligations in one place: serial access with a
// per-source interval, an identifying User-Agent, and robots.txt compliance. It
// only ever issues GET (DISCIPLINE §2). It mirrors driver/kokkaihttp; the small
// duplication is accepted to keep each source's boundary independent, and it adds
// GetAbs for the v1 updatelawlists fallback host and the cross-host roster pages.
//
// GetAbs is the one place an absolute, content-derived URL is fetched, so it is the
// SSRF chokepoint (CWE-918): the scheme must be http(s), the URL must carry no
// userinfo, and the host must be on the per-client allowlist (the base host plus
// any extra hosts passed to New). robots.txt is evaluated against the ACTUAL target
// host, not the base host, so the §7 compliance check matches the request.
package egovhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

// maxRedirects caps redirect chains; every hop is re-validated by checkRedirect.
const maxRedirects = 10

// maxBodyBytes bounds a single response body. Exceeding it is an error, not a
// silent truncation: a truncated body would still be content-hashed and appended
// to the immutable observation log as if it were the real resource (DISCIPLINE §3).
const maxBodyBytes = 64 << 20

// Throttle backoff bounds (DISCIPLINE §1): on 429/503 the whole source pauses for
// the upstream's Retry-After when present, else this fallback, capped so a
// malformed/hostile Retry-After cannot wedge the daemon indefinitely.
const (
	defaultThrottleBackoff = 60 * time.Second
	maxThrottleBackoff     = 15 * time.Minute
)

// Client serializes and spaces requests to one source. Construct one per source.
type Client struct {
	base     *url.URL
	ua       string
	interval time.Duration
	http     *http.Client
	allowed  map[string]struct{} // hostnames this client may reach (base + extras)

	mu   sync.Mutex // serializes requests (no parallel/burst access)
	next time.Time  // earliest time the next request may go out

	robots sync.Map // host(string) -> *robotsResult, fetched at most once per host
}

// robotsResult memoizes one host's robots.txt group. ready is set only after a
// successful fetch (parsed groups, or a confirmed 404 => allow-all) — a
// transient fetch error is NOT cached, so the next call retries instead of
// permanently failing every request to this host for the life of the process.
type robotsResult struct {
	mu    sync.Mutex
	ready bool
	group *robotstxt.Group
}

// New builds a client anchored on baseURL. The base host is always reachable;
// allowedHosts widens the GetAbs allowlist for sources that legitimately span more
// than one host (e.g. the 両院 roster on www.shugiin.go.jp + www.sangiin.go.jp).
func New(baseURL, userAgent string, interval time.Duration, allowedHosts ...string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	allowed := map[string]struct{}{strings.ToLower(u.Hostname()): {}}
	for _, h := range allowedHosts {
		if h != "" {
			allowed[strings.ToLower(h)] = struct{}{}
		}
	}
	c := &Client{
		base:     u,
		ua:       userAgent,
		interval: interval,
		allowed:  allowed,
	}
	c.http = &http.Client{Timeout: 30 * time.Second, CheckRedirect: c.checkRedirect}
	return c, nil
}

// checkRedirect re-applies the GetAbs validation to every redirect hop, so a
// redirecting upstream cannot steer the collector off the allowlist (SSRF,
// CWE-918): only http(s), no userinfo, and an allowlisted host. When the hop
// host's robots.txt is already cached the hop path is re-tested against it;
// fetching it here would bypass the serial interval gate, so an uncached (but
// allowlisted) host is covered by the host allowlist alone.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	u := req.URL
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("refusing redirect to non-http(s) scheme %q", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("refusing redirect with userinfo: %q", u.Redacted())
	}
	host := strings.ToLower(u.Hostname())
	if _, ok := c.allowed[host]; !ok {
		return fmt.Errorf("refusing redirect to off-allowlist host %q (SSRF guard)", host)
	}
	if g := c.cachedRobots(u.Host); g != nil && !g.Test(u.Path) {
		return fmt.Errorf("robots.txt disallows redirect target %q on %q", u.Path, host)
	}
	return nil
}

// cachedRobots returns the host's robots group when (and only when) its fetch has
// already succeeded; nil otherwise. Never fetches. Keyed like checkRobots (the
// URL's Host, which may carry a port).
func (c *Client) cachedRobots(host string) *robotstxt.Group {
	v, ok := c.robots.Load(strings.ToLower(host))
	if !ok {
		return nil
	}
	r := v.(*robotsResult)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ready {
		return nil
	}
	return r.group
}

// Get fetches base + "/" + endpoint with the given query, spacing requests by the
// per-source interval. A 404 is returned to the caller (not an error).
func (c *Client) Get(ctx context.Context, endpoint string, q url.Values) ([]byte, int, error) {
	target := *c.base
	target.Path = singleJoin(c.base.Path, endpoint)
	if q != nil {
		target.RawQuery = q.Encode()
	}
	// The target is built from the base, so its host is the (always-allowed) base host.
	return c.fetch(ctx, target.String(), c.base.Scheme, c.base.Host, target.Path)
}

// GetAbs fetches an absolute URL (the v1 updatelawlists fallback and the roster
// pages). It is the SSRF chokepoint: only http(s), no userinfo, and an
// allowlisted host may be fetched. It is subject to the same serial interval and
// robots policy as Get.
func (c *Client) GetAbs(ctx context.Context, rawURL string) ([]byte, int, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, 0, fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, 0, fmt.Errorf("refusing non-http(s) url scheme %q", u.Scheme)
	}
	if u.User != nil {
		return nil, 0, fmt.Errorf("refusing url with userinfo: %s", u.Redacted())
	}
	host := strings.ToLower(u.Hostname())
	if _, ok := c.allowed[host]; !ok {
		return nil, 0, fmt.Errorf("refusing off-allowlist host %q (SSRF guard)", host)
	}
	return c.fetch(ctx, u.String(), u.Scheme, u.Host, u.Path)
}

func (c *Client) fetch(ctx context.Context, rawURL, scheme, host, path string) ([]byte, int, error) {
	if err := c.checkRobots(ctx, scheme, host, path); err != nil {
		return nil, 0, err
	}
	return c.gatedDo(ctx, rawURL)
}

// gatedDo serializes the request behind the per-source mutex and waits out the
// interval since the previous request. Every outbound request — including the
// robots.txt fetches — goes through here, so first contact never bursts. A
// 429/503 extends the gate beyond the normal interval (via Retry-After when
// present), so every later request on this source — not just the one that got
// throttled — backs off (DISCIPLINE §1: never escalate into a block by retrying
// at the usual pace after an explicit throttle signal).
func (c *Client) gatedDo(ctx context.Context, rawURL string) ([]byte, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if wait := time.Until(c.next); wait > 0 {
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(wait):
		}
	}
	body, status, retryAfter, err := c.do(ctx, rawURL)
	c.next = time.Now().Add(nextInterval(c.interval, status, retryAfter))
	return body, status, err
}

// nextInterval is the normal per-source interval, except after a 429/503 where it
// is stretched to the upstream's Retry-After (or a fallback), capped at
// maxThrottleBackoff and never shorter than the normal interval.
func nextInterval(base time.Duration, status int, retryAfter time.Duration) time.Duration {
	if status != http.StatusTooManyRequests && status != http.StatusServiceUnavailable {
		return base
	}
	d := retryAfter
	if d <= 0 {
		d = defaultThrottleBackoff
	}
	if d > maxThrottleBackoff {
		d = maxThrottleBackoff
	}
	if d < base {
		d = base
	}
	return d
}

// retryAfterDuration parses Retry-After (RFC 9110 §10.2.3: delay-seconds or an
// HTTP-date). Returns 0 when absent, unparseable, or already past.
func retryAfterDuration(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func (c *Client) do(ctx context.Context, rawURL string) ([]byte, int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()
	// Read one byte past the cap: hitting it means the body was truncated, which
	// must fail loudly rather than silently hash+append a partial resource.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, resp.StatusCode, 0, err
	}
	if len(body) > maxBodyBytes {
		return nil, resp.StatusCode, 0, fmt.Errorf("response body exceeds %d byte limit", maxBodyBytes)
	}
	return body, resp.StatusCode, retryAfterDuration(resp), nil
}

// checkRobots lazily fetches and caches robots.txt for the TARGET host (not the
// base host) and rejects a disallowed path. A missing robots.txt is "allow all".
// The fetch consumes an interval slot (gatedDo) like any other request, so first
// contact with a host does not send two back-to-back requests. Only a successful
// fetch is cached — a transient fetch error is retried on the next call instead
// of permanently failing every request to this host for the life of the process.
func (c *Client) checkRobots(ctx context.Context, scheme, host, path string) error {
	v, _ := c.robots.LoadOrStore(strings.ToLower(host), &robotsResult{})
	r := v.(*robotsResult)

	r.mu.Lock()
	ready := r.ready
	r.mu.Unlock()

	if !ready {
		body, status, err := c.gatedDo(ctx, scheme+"://"+host+"/robots.txt")
		if err != nil {
			return fmt.Errorf("fetch robots.txt for %q: %w", host, err)
		}

		r.mu.Lock()
		if !r.ready {
			if status != http.StatusNotFound {
				data, perr := robotstxt.FromBytes(body)
				if perr != nil {
					r.mu.Unlock()
					return fmt.Errorf("parse robots.txt for %q: %w", host, perr)
				}
				r.group = data.FindGroup(c.ua)
			}
			r.ready = true
		}
		r.mu.Unlock()
	}

	r.mu.Lock()
	group := r.group
	r.mu.Unlock()
	if group != nil && !group.Test(path) {
		return fmt.Errorf("robots.txt disallows %q on %q", path, host)
	}
	return nil
}

func singleJoin(a, b string) string {
	switch {
	case a == "":
		return "/" + b
	case a[len(a)-1] == '/':
		return a + b
	default:
		return a + "/" + b
	}
}
