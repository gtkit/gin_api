package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/gtkit/logger/v2"
)

// This is a real TCP check of the production router, not a capacity benchmark.
func TestCapacityOverRealHTTPKeepsProbesAvailable(t *testing.T) {
	testutil.RequireLoopback(t)
	access, err := logger.New(logger.WithPath(filepath.Join(t.TempDir(), "access")), logger.WithFile(true), logger.WithConsole(false), logger.WithReplaceGlobals(false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(access.Sync)
	entered, release := make(chan struct{}), make(chan struct{})
	engine, err := newRouterForTest(t, "test", Options{AccessLogger: access, MaxInflightRequests: 1, RegisterModules: func(group *gin.RouterGroup) {
		group.GET("/hold", func(c *gin.Context) { close(entered); <-release; c.Status(http.StatusNoContent) })
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(engine)
	server.Start()
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 5 * time.Second
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := client.Get(server.URL + "/api/v1/hold")
		if err != nil {
			t.Error(err)
			return
		}
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	stop := sync.OnceFunc(func() { close(release); <-done })
	t.Cleanup(stop)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("held request did not enter")
	}
	var workers sync.WaitGroup
	var rejected atomic.Int64
	for range 64 {
		workers.Go(func() {
			response, err := client.Get(server.URL + "/api/v1/hold")
			if err != nil {
				t.Error(err)
				return
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			var body struct {
				Code      int    `json:"code"`
				RequestID string `json:"request_id"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if response.StatusCode != 503 || body.Code != 10010018 || body.RequestID == "" || body.RequestID != response.Header.Get("X-Request-ID") {
				t.Errorf("rejection=%d %+v", response.StatusCode, body)
				return
			}
			rejected.Add(1)
		})
	}
	workers.Wait()
	if rejected.Load() != 64 {
		t.Fatalf("rejected=%d", rejected.Load())
	}
	response, err := client.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != 200 {
		t.Fatalf("health status=%d read=%v close=%v", response.StatusCode, readErr, closeErr)
	}
	stop()
	t.Log("real TCP: 64 concurrent capacity rejections retained wire contract; health remained 200")
}
