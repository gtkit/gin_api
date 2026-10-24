package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/breaker"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/gtkit/httpc"
)

func TestClientResponseDecodeErrorsDoNotTripBreaker(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()
	for _, method := range []string{"Do", "DoIdempotent"} {
		for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity} {
			t.Run(method+"/"+strconv.Itoa(status), func(t *testing.T) {
				t.Parallel()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					if _, err := w.Write([]byte("not-json")); err != nil {
						t.Error(err)
					}
				}))
				t.Cleanup(server.Close)
				d := newTestDownstreams(t, server.URL, config.OutboundBreakerConfig{ConsecutiveFailures: 2})
				for range 3 {
					var decodeErr error
					attempt := func(ctx context.Context, client *httpc.Client) (int, error) {
						var body map[string]any
						code, err := client.GetJSON(ctx, "/", nil, &body)
						decodeErr = err
						return code, err
					}
					var gotStatus int
					var gotErr error
					if method == "Do" {
						gotStatus, gotErr = d.Do(t.Context(), "orders", attempt)
					} else {
						gotStatus, gotErr = d.DoIdempotent(t.Context(), "orders", RetryPolicy{Attempts: 1}, attempt)
					}
					if gotStatus != status || decodeErr == nil || !errors.Is(gotErr, decodeErr) {
						t.Fatalf("status=%d err=%v decode=%v; want original %d response and decode error", gotStatus, gotErr, decodeErr, status)
					}
				}
				called := false
				_, err := d.Do(t.Context(), "orders", func(context.Context, *httpc.Client) (int, error) {
					called = true
					return http.StatusOK, nil
				})
				if !called || breaker.IsOpen(err) || err != nil {
					t.Fatalf("healthy call was blocked by client responses: called=%v err=%v", called, err)
				}
			})
		}
	}
}

func TestClientResponseDoesNotResetPreviousBreakerFailure(t *testing.T) {
	t.Parallel()
	d := newTestDownstreams(t, "http://unused.example.test", config.OutboundBreakerConfig{ConsecutiveFailures: 2})
	serverFailure := func(context.Context, *httpc.Client) (int, error) { return http.StatusServiceUnavailable, nil }
	if _, err := d.Do(t.Context(), "orders", serverFailure); !errors.Is(err, ErrDownstreamServerError) {
		t.Fatal(err)
	}
	decodeErr := errors.New("decode client response")
	if _, err := d.Do(t.Context(), "orders", func(context.Context, *httpc.Client) (int, error) {
		return http.StatusBadRequest, decodeErr
	}); !errors.Is(err, decodeErr) {
		t.Fatal(err)
	}
	if _, err := d.Do(t.Context(), "orders", serverFailure); !errors.Is(err, ErrDownstreamServerError) {
		t.Fatal(err)
	}
	if _, err := d.Do(t.Context(), "orders", serverFailure); !breaker.IsOpen(err) {
		t.Fatalf("client response cleared a real dependency failure: %v", err)
	}
}
