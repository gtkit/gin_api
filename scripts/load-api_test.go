//go:build ignore

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type loadTransportFunc func(*http.Request) (*http.Response, error)

func (f loadTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoadConfigurationAndScenarioBounds(t *testing.T) {
	cfg := loadConfig{rate: 10, concurrency: 2, duration: time.Second, timeout: time.Second}
	for _, target := range []string{"http://example.com", "http://user:secret@127.0.0.1", "http://127.0.0.1/path", "file:///tmp/test", "http://0.0.0.0"} {
		if _, err := validateLoadConfig(target, cfg); err == nil {
			t.Fatalf("accepted invalid target %q", target)
		}
	}
	for _, target := range []string{"http://127.0.0.1:8008", "http://localhost", "https://[::1]"} {
		if _, err := validateLoadConfig(target, cfg); err != nil {
			t.Fatal(err)
		}
	}
	cfg.allowRemote = true
	if _, err := validateLoadConfig("https://example.com", cfg); err != nil {
		t.Fatal(err)
	}
	cfg.rate = 0
	if _, err := validateLoadConfig("http://127.0.0.1", cfg); err == nil {
		t.Fatal("zero rate accepted")
	}
	for _, body := range []string{`[]`, `[{"method":"GET","path":"http://example.com"}]`, `[{"method":"GET","path":"//example.com"}]`, `[{"method":"GET","path":"/","weight":-1}]`, `[{"method":"GET","path":"/","headers":{"Host":"example.com"}}]`, `[{}]`, strings.Repeat(" ", maxScenarioBytes+1)} {
		if _, err := readScenarios(strings.NewReader(body)); err == nil {
			t.Fatal("invalid scenario accepted")
		}
	}
	scenarios, err := readScenarios(strings.NewReader(`[{"method":"GET","path":"/health"}]`))
	if err != nil || scenarios[0].Weight != 1 {
		t.Fatalf("default weight: %v", err)
	}
}

func TestScheduledArrivalsIncludeOverdueSlots(t *testing.T) {
	for _, tc := range []struct {
		elapsed time.Duration
		want    int64
	}{{0, 1}, {99 * time.Millisecond, 1}, {350 * time.Millisecond, 4}, {time.Second, 10}, {time.Hour, 10}, {time.Duration(1<<63 - 1), 10}} {
		if got := scheduledCount(tc.elapsed, time.Second, 10); got != tc.want {
			t.Fatalf("%s: %d want %d", tc.elapsed, got, tc.want)
		}
	}
	if got := scheduledCount(24*time.Hour, 24*time.Hour, 100000); got != 8640000000 {
		t.Fatalf("upper duration/rate overflow: %d", got)
	}
}

func TestBoundedLoadAndCancellation(t *testing.T) {
	for _, earlyCancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "duration", true: "caller cancel"}[earlyCancel], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := loadConfig{rate: 10, concurrency: 1, duration: 5 * time.Second, timeout: time.Minute}
				var active, peak atomic.Int64
				client := &http.Client{Transport: loadTransportFunc(func(req *http.Request) (*http.Response, error) {
					v := active.Add(1)
					defer active.Add(-1)
					for p := peak.Load(); v > p && !peak.CompareAndSwap(p, v); p = peak.Load() {
					}
					select {
					case <-time.After(3 * time.Second):
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				})}
				base, err := url.Parse("http://127.0.0.1")
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if earlyCancel {
					go func() { synctest.Sleep(time.Second); cancel() }()
				}
				summary := runLoad(ctx, client, base, []loadScenario{{Method: "GET", Path: "/", Weight: 1}}, cfg)
				if summary.Sent+summary.Dropped != summary.Offered || summary.Completed != summary.Sent || summary.Dropped == 0 || peak.Load() > 1 || active.Load() != 0 {
					t.Fatalf("unbounded or lost work: %+v peak=%d", summary, peak.Load())
				}
				if !earlyCancel && summary.Offered != 50 {
					t.Fatalf("offered=%d", summary.Offered)
				}
				if earlyCancel && summary.ElapsedSeconds > 1.1 {
					t.Fatalf("late cancellation: %f", summary.ElapsedSeconds)
				}
			})
		})
	}
}

func TestLatencyStartsAtScheduledTimeAndOverflowIsExplicit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &http.Client{Transport: loadTransportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		})}
		base, err := url.Parse("http://127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		stats := loadStats{summary: loadSummary{Status: map[int]uint64{}}, buckets: make([]uint64, len(latencyBounds)+1)}
		stats.request(t.Context(), client, base, loadJob{scheduled: time.Now().Add(-4 * time.Second), scenario: loadScenario{Method: "GET", Path: "/"}})
		result := latencyResult(stats.buckets)
		if result.P99 == nil || *result.P99 != 5000 {
			t.Fatalf("scheduler latency omitted: %+v", result)
		}
	})
	counts := make([]uint64, len(latencyBounds)+1)
	counts[len(counts)-1] = 1
	result := latencyResult(counts)
	if result.P99 != nil || !result.P99Overflow {
		t.Fatalf("overflow silently finite: %+v", result)
	}
}

func TestCLIUsesRealLoopbackAndDoesNotLeakScenario(t *testing.T) {
	// This test needs a real loopback address to exercise the production dial
	// restriction; an in-memory httptest server's example.com URL would bypass it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://example.com/secret", 302)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "scenario.json")
	if err := os.WriteFile(file, []byte(`[{"method":"GET","path":"/redirect?token=must-not-leak","headers":{"Authorization":"Bearer must-not-leak"}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err := loadCLI(t.Context(), []string{"-target", server.URL, "-scenario", file, "-duration", "100ms", "-rate", "100", "-concurrency", "2"}, &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String()+errOut.String(), "must-not-leak") {
		t.Fatal("scenario leaked")
	}
	var summary loadSummary
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Status[302] == 0 || summary.TransportErrors != 0 || summary.Offered != 10 || summary.Sent+summary.Dropped != 10 {
		t.Fatalf("summary: %+v", summary)
	}
}
