package httpcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gincache"
)

type envelopeDeadlineTransport struct {
	http.ResponseWriter
	deadline time.Time
	cause    error
}

func (w *envelopeDeadlineTransport) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return w.cause
}

func TestEnvelopeWriterPreservesResponseControllerDeadlineChain(t *testing.T) {
	t.Parallel()
	cause := errors.New("transport write deadline failed")
	transport := &envelopeDeadlineTransport{ResponseWriter: httptest.NewRecorder(), cause: cause}
	c, _ := gin.CreateTestContext(transport)
	w := &envelopeWriter{ResponseWriter: c.Writer, ctx: c}
	deadline := time.Now().Add(time.Second)
	if err := http.NewResponseController(w).SetWriteDeadline(deadline); !errors.Is(err, cause) {
		t.Fatalf("deadline did not reach underlying transport: %v", err)
	}
	if !transport.deadline.Equal(deadline) {
		t.Fatal("transport did not receive original deadline")
	}
}

func envelopeEngine(cached gin.HandlerFunc, handler gin.HandlerFunc) *gin.Engine {
	engine := gin.New()
	var sequence atomic.Int64
	engine.Use(func(c *gin.Context) {
		id := fmt.Sprintf("request-%d", sequence.Add(1))
		c.Request = c.Request.WithContext(applog.ContextWithRequestID(c.Request.Context(), id))
		c.Header("X-Request-ID", id)
		c.Next()
	})
	engine.GET("/items", UnifiedJSONCache(cached), handler)
	return engine
}

func assertEnvelopeID(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := json.Unmarshal(body["request_id"], &id); err != nil {
		t.Fatal(err)
	}
	if id == "" || id != response.Header().Get("X-Request-ID") {
		t.Fatalf("body ID=%q header=%q", id, response.Header().Get("X-Request-ID"))
	}
}

func TestUnifiedJSONCacheRewritesOnlyTheCurrentEnvelope(t *testing.T) {
	t.Parallel()
	stager := newTestStager(t)
	calls := 0
	engine := envelopeEngine(stager.Middleware(0), func(c *gin.Context) {
		calls++
		c.Header("ETag", `"fixed"`)
		c.Header("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		c.Header("Content-Length", "999")
		c.Header("Content-Digest", "fixed")
		resp.Ok(c, json.RawMessage(`{"n":9007199254740993,"request_id":"nested-id"}`))
	})
	ids := map[string]bool{}
	for range 3 {
		request := httptest.NewRequest(http.MethodGet, "/items", nil)
		request.Header.Set("X-Request-ID", "forged")
		request.Header.Set("If-None-Match", "*")
		request.Header.Set("If-Modified-Since", "Wed, 21 Oct 2037 07:28:00 GMT")
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertEnvelopeID(t, response)
		id := response.Header().Get("X-Request-ID")
		if ids[id] {
			t.Fatal("replayed old request ID")
		}
		ids[id] = true
		if !strings.Contains(response.Body.String(), "9007199254740993") || !strings.Contains(response.Body.String(), `"request_id":"nested-id"`) {
			t.Fatal("data semantics changed")
		}
		for _, header := range []string{"Content-Length", "ETag", "Last-Modified", "Content-Digest"} {
			if response.Header().Get(header) != "" {
				t.Fatalf("stale %s", header)
			}
		}
	}
	if calls != 1 {
		t.Fatalf("handler calls=%d", calls)
	}
}

func TestUnifiedJSONCacheSharesDataAcrossConcurrentWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stager := newTestStager(t)
		var calls, shared atomic.Int64
		cached := gincache.Cache(stager.store, stager.ttl, gincache.WithCacheStrategyByRequest(cacheableRequest),
			gincache.WithOnShareSingleFlight(func(*gin.Context) { shared.Add(1) }))
		gate := make(chan struct{})
		engine := envelopeEngine(cached, func(c *gin.Context) { calls.Add(1); <-gate; resp.Ok(c, "shared") })
		responses := make([]*httptest.ResponseRecorder, 8)
		var group sync.WaitGroup
		for i := range responses {
			group.Go(func() {
				response := httptest.NewRecorder()
				responses[i] = response
				engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/items", nil))
			})
		}
		synctest.Wait()
		close(gate)
		group.Wait()
		ids := map[string]bool{}
		for _, response := range responses {
			assertEnvelopeID(t, response)
			id := response.Header().Get("X-Request-ID")
			if ids[id] {
				t.Fatal("shared ID")
			}
			ids[id] = true
		}
		if calls.Load() != 1 || shared.Load() == 0 {
			t.Fatalf("handler=%d shares=%d", calls.Load(), shared.Load())
		}
	})
}

func TestEnvelopeCacheHasASeparateKeyDomain(t *testing.T) {
	t.Parallel()
	stager := newTestStager(t)
	generic := gin.New()
	generic.GET("/items", stager.Middleware(0), func(c *gin.Context) { c.Header("ETag", `"generic"`); c.JSON(200, gin.H{"legacy": true}) })
	generic.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/items", nil))
	calls := 0
	unified := envelopeEngine(stager.Middleware(0), func(c *gin.Context) { calls++; resp.Ok(c, "current") })
	response := httptest.NewRecorder()
	unified.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/items", nil))
	assertEnvelopeID(t, response)
	if calls != 1 {
		t.Fatal("unified route consumed generic entry")
	}
	request := httptest.NewRequest(http.MethodGet, "/items", nil)
	request.Header.Set("If-None-Match", `"generic"`)
	response = httptest.NewRecorder()
	generic.ServeHTTP(response, request)
	if response.Code != 304 {
		t.Fatalf("generic validators changed: %d", response.Code)
	}
}

func TestEnvelopeCacheRejectsInvalidAndPreservesOversizeResponses(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"invalid", "invalid-json", "invalid-type", "invalid-media-type", "raw-oversize", "hijack", "explicit-headers", "compressed", "signed", "stream", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			stager := newTestStager(t)
			stager.maxBody = 128
			calls := 0
			engine := envelopeEngine(stager.Middleware(0), func(c *gin.Context) {
				calls++
				switch mode {
				case "invalid":
					c.JSON(200, gin.H{"ordinary": "payload"})
				case "invalid-json":
					c.Data(200, "application/json", []byte("{"))
				case "invalid-type":
					c.JSON(200, gin.H{"code": "wrong-type", "msg": "OK", "data": nil, "request_id": "stale"})
				case "invalid-media-type":
					c.Data(200, `application/json; charset="unterminated`, []byte(`{}`))
				case "raw-oversize":
					c.Data(200, "application/json", []byte(`{"code":0,"msg":"OK","data":"`+strings.Repeat("large", 100)+`","request_id":"stale"}`))
				case "hijack":
					connection, buffer, err := c.Writer.Hijack()
					if connection != nil || buffer != nil || !errors.Is(err, ErrInvalidEnvelope) {
						t.Errorf("hijack escaped envelope contract: connection=%v buffer=%v err=%v", connection, buffer, err)
					}
					if connection != nil {
						if closeErr := connection.Close(); closeErr != nil {
							t.Errorf("close unexpectedly hijacked connection: %v", closeErr)
						}
					}
				case "signed":
					c.Header("Signature", "signed-bytes")
					resp.Ok(c, "data")
				case "compressed":
					c.Header("Content-Encoding", "gzip")
					resp.Ok(c, "data")
				case "stream":
					c.Writer.Flush()
				case "explicit-headers":
					c.Writer.WriteHeaderNow()
				case "oversize":
					resp.Ok(c, strings.Repeat("large", 100))
				}
			})
			for range 2 {
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/items", nil))
				want := 500
				if mode == "oversize" {
					want = 200
				}
				if response.Code != want {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				assertEnvelopeID(t, response)
				if response.Header().Get("Content-Encoding") != "" || response.Header().Get("Signature") != "" {
					t.Fatal("fallback kept incompatible representation headers")
				}
			}
			if calls != 2 {
				t.Fatalf("invalid/oversize response cached: calls=%d", calls)
			}
		})
	}
}

func TestEnvelopeCacheWriteStringPreservesCurrentIDAndPayload(t *testing.T) {
	t.Parallel()
	stager := newTestStager(t)
	calls := 0
	raw := `{"code":0,"msg":"OK","data":{"n":9007199254740993},"request_id":"stale"}`
	engine := envelopeEngine(stager.Middleware(0), func(c *gin.Context) {
		calls++
		c.Header("Content-Type", "application/json")
		if n, err := c.Writer.WriteString(raw); err != nil || n != len(raw) {
			t.Errorf("write ordinary envelope string: n=%d err=%v", n, err)
		}
	})
	for range 2 {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/items", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertEnvelopeID(t, response)
		if !strings.Contains(response.Body.String(), "9007199254740993") || strings.Contains(response.Body.String(), "stale") {
			t.Fatalf("envelope data/ID changed: %s", response.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("ordinary envelope string was not cached: calls=%d", calls)
	}
}

func TestEnvelopeCacheWithoutServerIDClearsStaleCorrelation(t *testing.T) {
	t.Parallel()
	stager := newTestStager(t)
	engine := gin.New()
	calls := 0
	engine.GET("/items", UnifiedJSONCache(stager.Middleware(0)), func(c *gin.Context) {
		calls++
		c.Header("X-Request-ID", "stale")
		resp.Ok(c, "data")
	})
	for range 2 {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/items", nil)
		request.Header.Set("X-Request-ID", "client-forged")
		engine.ServeHTTP(response, request)
		var envelope resp.ResResult
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || envelope.RequestID != "" || response.Header().Get("X-Request-ID") != "" {
			t.Fatalf("unassigned correlation status=%d header=%q envelope=%+v", response.Code, response.Header().Get("X-Request-ID"), envelope)
		}
	}
	if calls != 1 {
		t.Fatalf("ordinary response not cached: calls=%d", calls)
	}
}

func TestEnvelopeCachePreservesBodylessStatuses(t *testing.T) {
	t.Parallel()
	stager := newTestStager(t)
	for _, status := range []int{204, 304} {
		engine := envelopeEngine(stager.Middleware(0), func(c *gin.Context) { c.Status(status) })
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/items", nil))
		if response.Code != status || response.Body.Len() != 0 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	engine := gin.New()
	engine.HEAD("/head", UnifiedJSONCache(stager.Middleware(0)), func(c *gin.Context) { resp.Ok(c, "must not write") })
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodHead, "/head", nil))
	if response.Body.Len() != 0 {
		t.Fatal("HEAD body written")
	}
}

type failingEnvelopeHTTPWriter struct {
	http.ResponseWriter
	failure error
}

func (w failingEnvelopeHTTPWriter) Write([]byte) (int, error) { return 0, w.failure }

type envelopeLogCapture struct{ entries []applog.LogEntry }

func (s *envelopeLogCapture) Log(_ context.Context, e applog.LogEntry) {
	s.entries = append(s.entries, e)
}

func TestEnvelopeCacheObservesAnIgnoredReplayWriteError(t *testing.T) {
	sink := &envelopeLogCapture{}
	release, err := applog.Install(sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stager := newTestStager(t)
	engine := envelopeEngine(stager.Middleware(0), func(c *gin.Context) { resp.Ok(c, "cached") })
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/items", nil))
	failure := errors.New("socket write failed")
	engine.ServeHTTP(failingEnvelopeHTTPWriter{httptest.NewRecorder(), failure}, httptest.NewRequest(http.MethodGet, "/items", nil))
	if len(sink.entries) != 1 {
		t.Fatalf("write error events=%d", len(sink.entries))
	}
	found := false
	for _, f := range sink.entries[0].Fields {
		if text, ok := f.Value.(string); ok && strings.Contains(text, "socket write failed") {
			found = true
		}
	}
	if !found {
		t.Fatal("cache replay write error lost")
	}
}
