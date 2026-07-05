package egovhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestServer serves robots.txt as 404 (allow all) plus the given handlers, and
// records the arrival time of every request so interval gating can be asserted.
func newTestServer(t *testing.T, handlers map[string]http.HandlerFunc) (*httptest.Server, func() []time.Time) {
	t.Helper()
	var mu sync.Mutex
	var times []time.Time
	mux := http.NewServeMux()
	for pattern, h := range handlers {
		mux.HandleFunc(pattern, h)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []time.Time {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Time(nil), times...)
	}
}

// A redirect within the allowlisted host is followed and the final body returned.
func TestGetFollowsRedirectWithinAllowlistedHost(t *testing.T) {
	srv, _ := newTestServer(t, map[string]http.HandlerFunc{
		"/hop": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/target", http.StatusFound)
		},
		"/target": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		},
	})
	c, err := New(srv.URL, "test-agent", 0)
	if err != nil {
		t.Fatal(err)
	}
	body, status, err := c.Get(context.Background(), "hop", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status != 200 || string(body) != "ok" {
		t.Fatalf("status=%d body=%q, want 200 \"ok\"", status, body)
	}
}

// A redirect to an off-allowlist host must be refused: the SSRF allowlist applies
// to every hop, not just the initial URL (Get and GetAbs share the transport).
// The off-host target is never dialed (example.invalid would not resolve anyway).
func TestGetAbsRefusesRedirectToOffAllowlistHost(t *testing.T) {
	srv, _ := newTestServer(t, map[string]http.HandlerFunc{
		"/hop": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://attacker.example.invalid/exfil", http.StatusFound)
		},
	})
	c, err := New(srv.URL, "test-agent", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.GetAbs(context.Background(), srv.URL+"/hop")
	if err == nil || !strings.Contains(err.Error(), "off-allowlist") {
		t.Fatalf("redirect to an off-allowlist host must be refused, got err=%v", err)
	}
}

// A redirect to a non-http(s) scheme must be refused.
func TestGetRefusesRedirectToNonHTTPScheme(t *testing.T) {
	srv, _ := newTestServer(t, map[string]http.HandlerFunc{
		"/hop": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "ftp://127.0.0.1/pub", http.StatusFound)
		},
	})
	c, err := New(srv.URL, "test-agent", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Get(context.Background(), "hop", nil)
	if err == nil || !strings.Contains(err.Error(), "non-http(s)") {
		t.Fatalf("redirect to a non-http(s) scheme must be refused, got err=%v", err)
	}
}

// The robots.txt fetch consumes an interval slot: first contact with a host must
// not send the robots GET and the payload GET back-to-back (DISCIPLINE §1).
func TestRobotsFetchConsumesIntervalSlot(t *testing.T) {
	srv, requestTimes := newTestServer(t, map[string]http.HandlerFunc{
		"/target": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		},
	})
	const interval = 150 * time.Millisecond
	c, err := New(srv.URL, "test-agent", interval)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Get(context.Background(), "target", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	times := requestTimes()
	if len(times) != 2 {
		t.Fatalf("requests = %d, want 2 (robots.txt + target)", len(times))
	}
	if gap := times[1].Sub(times[0]); gap < interval {
		t.Fatalf("gap between robots.txt and target = %v, want >= %v", gap, interval)
	}
}

// A redirect target disallowed by the (already cached) robots.txt is refused.
func TestRedirectHopHonorsCachedRobots(t *testing.T) {
	mux := map[string]http.HandlerFunc{
		"/robots.txt": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		},
		"/hop": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/private/secret", http.StatusFound)
		},
	}
	srv, _ := newTestServer(t, mux)
	c, err := New(srv.URL, "test-agent", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Get(context.Background(), "hop", nil)
	if err == nil || !strings.Contains(err.Error(), "robots.txt disallows redirect target") {
		t.Fatalf("redirect into a robots-disallowed path must be refused, got err=%v", err)
	}
}

// A transient robots.txt fetch failure (e.g. a network blip on first contact)
// must not permanently disallow every later request to the host: the next call
// has to retry the fetch, not replay a cached error forever.
func TestRobotsFetchErrorIsRetriedNotCachedForever(t *testing.T) {
	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			conn.Close() // abrupt close on first fetch => client sees a transport error
			return
		}
		w.WriteHeader(http.StatusNotFound) // second+ fetch: allow all
	})
	mux.HandleFunc("/target", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "test-agent", 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := c.Get(context.Background(), "target", nil); err == nil {
		t.Fatal("first Get: want error from the robots.txt fetch failure, got nil")
	}

	body, status, err := c.Get(context.Background(), "target", nil)
	if err != nil {
		t.Fatalf("second Get: want success after the robots.txt retry, got err=%v", err)
	}
	if status != 200 || string(body) != "ok" {
		t.Fatalf("status=%d body=%q, want 200 \"ok\"", status, body)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("robots.txt fetched %d times, want 2 (retried after the first failure)", n)
	}
}

// A 429/503 response extends the source-wide gate (via Retry-After) so the very
// next request — not just a retry of the throttled one — waits it out, instead
// of hammering the source again at the normal interval.
func TestGetOn429ExtendsGateForNextRequest(t *testing.T) {
	var calls int32
	srv, requestTimes := newTestServer(t, map[string]http.HandlerFunc{
		"/target": func(w http.ResponseWriter, _ *http.Request) {
			if atomic.AddInt32(&calls, 1) == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte("ok"))
		},
	})
	c, err := New(srv.URL, "test-agent", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, status, err := c.Get(context.Background(), "target", nil); err != nil || status != http.StatusTooManyRequests {
		t.Fatalf("first Get: status=%d err=%v, want 429/nil", status, err)
	}
	if _, _, err := c.Get(context.Background(), "target", nil); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	times := requestTimes() // [0]=robots.txt [1]=first /target (429) [2]=second /target
	if len(times) != 3 {
		t.Fatalf("requests = %d, want 3", len(times))
	}
	if gap := times[2].Sub(times[1]); gap < 900*time.Millisecond {
		t.Fatalf("gap after 429 = %v, want >= ~1s (Retry-After)", gap)
	}
}

func TestNextIntervalHonorsRetryAfterOnThrottle(t *testing.T) {
	if got := nextInterval(0, http.StatusTooManyRequests, 5*time.Second); got != 5*time.Second {
		t.Fatalf("nextInterval = %v, want 5s (Retry-After)", got)
	}
	if got := nextInterval(0, http.StatusServiceUnavailable, 5*time.Second); got != 5*time.Second {
		t.Fatalf("nextInterval = %v, want 5s (Retry-After) on 503", got)
	}
}

func TestNextIntervalFallsBackWithoutRetryAfter(t *testing.T) {
	if got := nextInterval(0, http.StatusTooManyRequests, 0); got != defaultThrottleBackoff {
		t.Fatalf("nextInterval = %v, want default %v", got, defaultThrottleBackoff)
	}
}

func TestNextIntervalCapsRetryAfter(t *testing.T) {
	if got := nextInterval(0, http.StatusTooManyRequests, 30*time.Minute); got != maxThrottleBackoff {
		t.Fatalf("nextInterval = %v, want cap %v", got, maxThrottleBackoff)
	}
}

func TestNextIntervalUnaffectedOnSuccess(t *testing.T) {
	if got := nextInterval(3*time.Second, http.StatusOK, 999*time.Second); got != 3*time.Second {
		t.Fatalf("nextInterval = %v, want base 3s unaffected by Retry-After on 200", got)
	}
}
