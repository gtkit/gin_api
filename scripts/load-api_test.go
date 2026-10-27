//go:build ignore

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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

func loadDropCounts(t *testing.T, summary loadSummary) (uint64, uint64) {
	t.Helper()
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		Scheduler *uint64 `json:"scheduler_dropped"`
		Worker    *uint64 `json:"worker_dropped"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields.Scheduler == nil || fields.Worker == nil {
		t.Fatalf("missing drop classification: %s", raw)
	}
	if summary.Dropped != *fields.Scheduler+*fields.Worker || summary.Offered != summary.Sent+summary.Dropped || summary.Completed != summary.Sent {
		t.Fatalf("lost work: %+v, classifications=%s", summary, raw)
	}
	return *fields.Scheduler, *fields.Worker
}

type loadContextBody struct{ ctx context.Context }

func (b loadContextBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, context.Cause(b.ctx)
}

func (loadContextBody) Close() error { return nil }

func assertLoadAccounting(t *testing.T, summary loadSummary) {
	t.Helper()
	loadDropCounts(t, summary)
	var samples, statuses uint64
	for _, count := range summary.Latency.Counts {
		samples += count
	}
	for _, count := range summary.Status {
		statuses += count
	}
	if summary.CanceledRequests > summary.Completed || samples != summary.Completed-summary.CanceledRequests || statuses > summary.Completed {
		t.Fatalf("inconsistent cancellation/status/latency counts: %+v", summary)
	}
}

func TestLoadCancellationAccounting(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		body                         bool
		cancelAfter                  time.Duration
		parentDeadline               bool
		realIOError                  bool
		independentCancel            bool
		reason                       string
		canceled, failures, statuses uint64
	}{
		{name: "cancel while generating", cancelAfter: 500 * time.Millisecond, reason: "canceled", canceled: 1},
		{name: "cancel body while draining", body: true, cancelAfter: 1500 * time.Millisecond, reason: "canceled", canceled: 1, statuses: 1},
		{name: "parent deadline while draining", body: true, parentDeadline: true, reason: "deadline_exceeded", failures: 1, statuses: 1},
		{name: "request header timeout", reason: "completed", failures: 1},
		{name: "request body timeout", body: true, reason: "completed", failures: 1, statuses: 1},
		{name: "I/O failure before cancellation classification", realIOError: true, reason: "canceled", failures: 1},
		{name: "unrelated canceled error", independentCancel: true, reason: "completed", failures: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var ctx context.Context
				var cancel context.CancelFunc
				if tc.parentDeadline {
					ctx, cancel = context.WithTimeout(t.Context(), 1500*time.Millisecond)
				} else {
					ctx, cancel = context.WithCancel(t.Context())
				}
				defer cancel()
				if tc.cancelAfter > 0 {
					go func() { synctest.Sleep(tc.cancelAfter); cancel() }()
				}
				client := &http.Client{Transport: loadTransportFunc(func(req *http.Request) (*http.Response, error) {
					if tc.realIOError {
						cancel()
						return nil, io.ErrUnexpectedEOF
					}
					if tc.independentCancel {
						return nil, context.Canceled
					}
					if tc.body {
						return &http.Response{StatusCode: http.StatusOK, Body: loadContextBody{req.Context()}}, nil
					}
					<-req.Context().Done()
					return nil, fmt.Errorf("fixture: %w", context.Cause(req.Context()))
				})}
				base, err := url.Parse("http://127.0.0.1")
				if err != nil {
					t.Fatal(err)
				}
				cfg := loadConfig{rate: 1, concurrency: 1, duration: time.Second, timeout: 2 * time.Second}
				summary := runLoadWithClock(ctx, client, base, []loadScenario{{Method: http.MethodGet, Path: "/", Weight: 1}}, cfg, func() time.Time {
					synctest.Wait()
					return time.Now()
				})
				if summary.Sent != 1 || summary.TerminationReason != tc.reason || summary.CanceledRequests != tc.canceled || summary.TransportErrors != tc.failures || summary.Status[http.StatusOK] != tc.statuses {
					t.Fatalf("wrong termination/accounting: %+v", summary)
				}
				assertLoadAccounting(t, summary)
			})
		})
	}
}

type loadErrorWriter struct{ err error }

func (w loadErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestLoadCLIInterruptedJSONAndError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "scenario.json")
	if err := os.WriteFile(file, []byte(`[{"method":"GET","path":"/"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-target", "http://127.0.0.1:1", "-scenario", file}
	for _, reason := range []string{"canceled", "deadline_exceeded"} {
		t.Run(reason, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			want := context.Canceled
			if reason == "canceled" {
				ctx, cancel = context.WithCancel(t.Context())
				cancel()
			} else {
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			}
			defer cancel()
			var out, errOut bytes.Buffer
			err := loadCLI(ctx, args, &out, &errOut)
			if !errors.Is(err, want) {
				t.Errorf("CLI error = %v, want %v", err, want)
			}
			var summary loadSummary
			if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
				t.Fatal(err)
			}
			if summary.TerminationReason != reason || summary.Sent != 0 {
				t.Fatalf("missing interrupted result: %+v", summary)
			}
			assertLoadAccounting(t, summary)
			if err := loadCLI(ctx, args, loadErrorWriter{io.ErrClosedPipe}, &errOut); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("encoding error was hidden: %v", err)
			}
		})
	}
	var out, errOut bytes.Buffer
	if err := loadCLI(t.Context(), []string{"-rate", "0"}, &out, &errOut); err == nil || out.Len() != 0 {
		t.Fatalf("invalid arguments produced a result: %v %s", err, out.String())
	}
}

func TestLoadClientTimeoutAfterResponseHeaders(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		<-req.Context().Done()
	}))
	server.Start()
	t.Cleanup(server.Close)
	cfg := loadConfig{rate: 100, concurrency: 1, duration: 100 * time.Millisecond, timeout: time.Second}
	client, transport := loadHTTPClient(cfg)
	client.Timeout = 200 * time.Millisecond // Force Client.Timeout before the request context expires.
	t.Cleanup(transport.CloseIdleConnections)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	summary := runLoad(t.Context(), client, base, []loadScenario{{Method: http.MethodGet, Path: "/", Weight: 1}}, cfg)
	if summary.Sent == 0 || summary.TerminationReason != "completed" || summary.CanceledRequests != 0 || summary.TransportErrors != summary.Sent || summary.Status[http.StatusOK] != summary.Sent {
		t.Fatalf("client timeout was misclassified: %+v", summary)
	}
	assertLoadAccounting(t, summary)
}

func TestLoadMainExitCodes(t *testing.T) {
	// Execute the production entry point itself, not a test helper or go run.
	binary := filepath.Join(t.TempDir(), "load-api")
	buildCtx, cancelBuild := context.WithTimeout(t.Context(), time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildCtx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, "load-api.go")
	build.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build load-api: %v\n%s", err, output)
	}
	for _, tc := range []struct {
		name        string
		signal      os.Signal
		invalidArgs bool
		badOutput   string
		status      int
		wantCode    int
		wantReason  string
	}{
		{name: "completed", status: http.StatusNoContent, wantReason: "completed"},
		{name: "HTTP failure still completes", status: http.StatusServiceUnavailable, wantReason: "completed"},
		{name: "invalid arguments", invalidArgs: true, wantCode: 1},
		{name: "SIGINT", signal: os.Interrupt, wantCode: 1, wantReason: "canceled"},
		{name: "SIGTERM", signal: syscall.SIGTERM, wantCode: 1, wantReason: "canceled"},
		{name: "stdout failure", badOutput: "stdout", status: http.StatusNoContent, wantCode: 1},
		{name: "stderr failure", invalidArgs: true, badOutput: "stderr", wantCode: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{})
			var first atomic.Bool
			// A subprocess cannot reach an in-memory httptest server.
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if first.CompareAndSwap(false, true) {
					close(started)
				}
				if tc.signal != nil {
					<-req.Context().Done()
					return
				}
				w.WriteHeader(tc.status)
			}))
			server.Start()
			t.Cleanup(server.Close)
			file := filepath.Join(t.TempDir(), "scenario.json")
			if err := os.WriteFile(file, []byte(`[{"method":"GET","path":"/"}]`), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"-target", server.URL, "-scenario", file, "-duration", "200ms", "-rate", "100", "-concurrency", "1", "-timeout", "5s"}
			if tc.signal != nil {
				args = append(args, "-duration", "30s")
			}
			if tc.invalidArgs {
				args = []string{"-rate", "0"}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, args...)
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			if tc.badOutput != "" {
				// Read-only fd yields a write error without platform-specific /dev/full.
				unwritable, err := os.Open(os.DevNull)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := unwritable.Close(); err != nil {
						t.Error(err)
					}
				})
				if tc.badOutput == "stdout" {
					cmd.Stdout = unwritable
				} else {
					cmd.Stderr = unwritable
				}
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var waitErr error
			var waiter sync.WaitGroup
			waiter.Go(func() {
				waitErr = cmd.Wait()
				close(done)
			})
			t.Cleanup(func() { cancel(); waiter.Wait() })
			if tc.signal != nil {
				select {
				case <-started:
					if err := cmd.Process.Signal(tc.signal); err != nil {
						t.Fatal(err)
					}
				case <-done:
					t.Fatalf("process exited before request: %v\n%s", waitErr, errOut.String())
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			<-done
			if ctx.Err() != nil {
				t.Fatalf("subprocess timed out: %v", ctx.Err())
			}
			code := 0
			if waitErr != nil {
				exitErr, ok := errors.AsType[*exec.ExitError](waitErr)
				if !ok {
					t.Fatal(waitErr)
				}
				code = exitErr.ExitCode()
			}
			if code != tc.wantCode {
				t.Fatalf("exit=%d, want %d; stdout=%s stderr=%s", code, tc.wantCode, out.String(), errOut.String())
			}
			if tc.wantReason == "" {
				if out.Len() != 0 {
					t.Fatalf("unexpected result JSON: %s", out.String())
				}
			} else {
				var summary loadSummary
				if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
					t.Fatal(err)
				}
				if summary.TerminationReason != tc.wantReason || summary.Sent == 0 {
					t.Fatalf("wrong subprocess result: %+v", summary)
				}
				assertLoadAccounting(t, summary)
				if tc.signal != nil {
					if summary.CanceledRequests != summary.Completed || summary.TransportErrors != 0 {
						t.Fatalf("signal was misclassified: %+v", summary)
					}
				} else if summary.Status[tc.status] != summary.Completed || summary.TransportErrors != 0 {
					t.Fatalf("HTTP outcome changed exit semantics: %+v", summary)
				}
			}
			if tc.wantCode == 0 && errOut.Len() != 0 || tc.wantCode != 0 && tc.badOutput != "stderr" && errOut.Len() == 0 {
				t.Fatalf("unexpected stderr: %s", errOut.String())
			}
		})
	}
}

func TestLoadDrainsWithoutCancelingAtGenerationEnd(t *testing.T) {
	for _, requestTimesOut := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%t", requestTimesOut), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := loadConfig{rate: 1, concurrency: 1, duration: time.Second, timeout: 2 * time.Second}
				client := &http.Client{Transport: loadTransportFunc(func(req *http.Request) (*http.Response, error) {
					if requestTimesOut {
						<-req.Context().Done()
						return nil, req.Context().Err()
					}
					select {
					case <-time.After(1500 * time.Millisecond):
						return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				})}
				base, err := url.Parse("http://127.0.0.1")
				if err != nil {
					t.Fatal(err)
				}
				summary := runLoadWithClock(t.Context(), client, base, []loadScenario{{Method: http.MethodGet, Path: "/", Weight: 1}}, cfg, func() time.Time {
					synctest.Wait() // Workers are waiting before the first planned arrival.
					return time.Now()
				})
				if summary.Sent != 1 || summary.Completed != 1 {
					t.Fatalf("missing in-flight request: %+v", summary)
				}
				if requestTimesOut {
					if summary.TransportErrors != 1 || summary.ElapsedSeconds != 2 {
						t.Fatalf("request timeout not preserved: %+v", summary)
					}
				} else if summary.TransportErrors != 0 || summary.Status[http.StatusOK] != 1 || summary.ElapsedSeconds != 1.5 {
					t.Fatalf("generation expiry truncated request: %+v", summary)
				}
				if summary.Latency.P99 == nil || *summary.Latency.P99 != 2000 {
					t.Fatalf("truncated latency: %+v", summary.Latency)
				}
			})
		})
	}
}

func TestLoadSeparatesSchedulerAndWorkerDrops(t *testing.T) {
	for _, schedulerDelay := range []bool{true, false} {
		t.Run(fmt.Sprintf("scheduler_delay=%t", schedulerDelay), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := loadConfig{rate: 10, concurrency: 1, duration: time.Second, timeout: 3 * time.Second}
				client := &http.Client{Transport: loadTransportFunc(func(req *http.Request) (*http.Response, error) {
					if !schedulerDelay {
						select {
						case <-time.After(2 * time.Second):
						case <-req.Context().Done():
							return nil, req.Context().Err()
						}
					}
					return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
				})}
				base, err := url.Parse("http://127.0.0.1")
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				summary := runLoadWithClock(t.Context(), client, base, []loadScenario{{Method: http.MethodGet, Path: "/", Weight: 1}}, cfg, func() time.Time {
					calls++
					if calls == 2 && schedulerDelay {
						synctest.Sleep(350 * time.Millisecond)
					}
					synctest.Wait()
					return time.Now()
				})
				scheduler, worker := loadDropCounts(t, summary)
				if summary.Offered != 10 {
					t.Fatalf("offered=%d, want 10", summary.Offered)
				}
				if schedulerDelay {
					if scheduler != 3 || worker != 0 || summary.Sent != 7 {
						t.Fatalf("scheduler loss misclassified: %+v", summary)
					}
				} else if scheduler != 0 || worker != 9 || summary.Sent != 1 {
					t.Fatalf("worker loss misclassified: %+v", summary)
				}
			})
		})
	}
}

func TestLoadDrainsRealHTTPResponseBody(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		timer := time.NewTimer(300 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			if _, err := w.Write([]byte("complete")); err != nil {
				return
			}
		case <-req.Context().Done():
		}
	}))
	server.Start() // A real loopback URL exercises the tool's production transport.
	t.Cleanup(server.Close)
	cfg := loadConfig{rate: 50, concurrency: 2, duration: 200 * time.Millisecond, timeout: 2 * time.Second}
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, transport := loadHTTPClient(cfg)
	t.Cleanup(transport.CloseIdleConnections)
	summary := runLoad(t.Context(), client, base, []loadScenario{{Method: http.MethodGet, Path: "/", Weight: 1}}, cfg)
	if summary.Sent == 0 || summary.Completed != summary.Sent || summary.TransportErrors != 0 || summary.Status[http.StatusOK] != summary.Sent || summary.ElapsedSeconds < .3 {
		t.Fatalf("HTTP body was cut off by generation expiry: %+v", summary)
	}
}

func TestLoadDoesNotDispatchAfterWindowEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := loadConfig{rate: 10, concurrency: 1, duration: time.Second, timeout: time.Second}
		var requests atomic.Int64
		client := &http.Client{Transport: loadTransportFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		})}
		base, err := url.Parse("http://127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		summary := runLoadWithClock(t.Context(), client, base, []loadScenario{{Method: http.MethodGet, Path: "/", Weight: 1}}, cfg, func() time.Time {
			calls++
			if calls == 2 {
				synctest.Sleep(1100 * time.Millisecond)
			}
			synctest.Wait()
			return time.Now()
		})
		scheduler, worker := loadDropCounts(t, summary)
		if requests.Load() != 0 || summary.Sent != 0 || scheduler != 10 || worker != 0 {
			t.Fatalf("dispatched after generation ended: %+v, requests=%d", summary, requests.Load())
		}
	})
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
		stats.request(t.Context(), client, base, loadJob{scheduled: time.Now().Add(-4 * time.Second), scenario: loadScenario{Method: "GET", Path: "/"}}, time.Second)
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
