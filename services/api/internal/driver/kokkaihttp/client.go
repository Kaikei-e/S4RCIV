// Package kokkaihttp is the read-only HTTP-GET boundary to the kokkai
// (国会会議録検索API). It enforces the DISCIPLINE §1 obligations in one place:
// serial access with a per-source interval, an identifying User-Agent, and
// robots.txt compliance. It only ever issues GET (DISCIPLINE §2).
package kokkaihttp

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

	mu   sync.Mutex // serializes requests (no parallel/burst access)
	next time.Time  // earliest time the next request may go out

	robotsMu    sync.Mutex // guards robots below; distinct from mu (gatedDo already holds mu)
	robotsReady bool       // set only on a successful fetch (or a confirmed 404 => allow-all)
	robots      *robotstxt.Group
}

func New(baseURL, userAgent string, interval time.Duration) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	c := &Client{
		base:     u,
		ua:       userAgent,
		interval: interval,
	}
	c.http = &http.Client{Timeout: 30 * time.Second, CheckRedirect: c.checkRedirect}
	return c, nil
}

// checkRedirect re-applies the initial-request validation to every redirect hop,
// so a redirecting upstream cannot steer the collector off the source (SSRF,
// CWE-918): the scheme must stay the base scheme, the URL must carry no userinfo,
// and the host must stay the base host. The base host's robots.txt is cached
// before any non-robots request runs, so the hop path is re-tested without a fetch.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	u := req.URL
	if u.Scheme != c.base.Scheme {
		return fmt.Errorf("refusing redirect to scheme %q (want %q)", u.Scheme, c.base.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("refusing redirect with userinfo: %q", u.Redacted())
	}
	if !strings.EqualFold(u.Hostname(), c.base.Hostname()) {
		return fmt.Errorf("refusing redirect off the source host: %q (SSRF guard)", u.Hostname())
	}
	c.robotsMu.Lock()
	robots := c.robots
	c.robotsMu.Unlock()
	if robots != nil && !robots.Test(u.Path) {
		return fmt.Errorf("robots.txt disallows redirect target %q", u.Path)
	}
	return nil
}

// Get fetches base + "/" + endpoint with the given query. It blocks until the
// per-source interval has elapsed since the previous request. Returns the body
// and HTTP status; a 404 is returned to the caller (not an error) so a vanished
// resource can be recorded.
func (c *Client) Get(ctx context.Context, endpoint string, q url.Values) ([]byte, int, error) {
	target := *c.base
	target.Path = singleJoin(c.base.Path, endpoint)
	target.RawQuery = q.Encode()

	if err := c.checkRobots(ctx, target.Path); err != nil {
		return nil, 0, err
	}
	return c.gatedDo(ctx, target.String())
}

// gatedDo serializes the request behind the per-source mutex and waits out the
// interval since the previous request. Every outbound request — including the
// robots.txt fetch — goes through here, so first contact never bursts. A 429/503
// extends the gate beyond the normal interval (via Retry-After when present), so
// every later request on this source — not just the one that got throttled —
// backs off (DISCIPLINE §1: never escalate into a block by retrying at the usual
// pace after an explicit throttle signal).
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

// checkRobots lazily fetches and caches robots.txt for the host and rejects a
// disallowed path. A missing robots.txt is treated as "allow all". The fetch
// consumes an interval slot (gatedDo) like any other request, so first contact
// does not send two back-to-back requests. Only a successful fetch (parsed
// groups, or a confirmed 404) is cached — a transient fetch error (network blip)
// is retried on the next call instead of permanently failing every request on
// this source for the life of the process.
func (c *Client) checkRobots(ctx context.Context, path string) error {
	c.robotsMu.Lock()
	ready := c.robotsReady
	c.robotsMu.Unlock()

	if !ready {
		robotsURL := c.base.Scheme + "://" + c.base.Host + "/robots.txt"
		body, status, err := c.gatedDo(ctx, robotsURL)
		if err != nil {
			return fmt.Errorf("fetch robots.txt: %w", err)
		}

		c.robotsMu.Lock()
		if !c.robotsReady {
			if status != http.StatusNotFound {
				data, perr := robotstxt.FromBytes(body)
				if perr != nil {
					c.robotsMu.Unlock()
					return fmt.Errorf("parse robots.txt: %w", perr)
				}
				c.robots = data.FindGroup(c.ua)
			}
			c.robotsReady = true
		}
		c.robotsMu.Unlock()
	}

	c.robotsMu.Lock()
	robots := c.robots
	c.robotsMu.Unlock()
	if robots != nil && !robots.Test(path) {
		return fmt.Errorf("robots.txt disallows %s", path)
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
