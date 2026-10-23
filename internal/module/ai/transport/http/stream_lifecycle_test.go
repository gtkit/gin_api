package http

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/module/ai/application"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
)

type lifecycleLLM struct {
	stubLLM
	stream func(context.Context) (application.Reply, error)
}

func (l *lifecycleLLM) Stream(ctx context.Context, _ application.ChatCall, _ func(string) error) (application.Reply, error) {
	return l.stream(ctx)
}

func lifecycleHandler(t *testing.T, llm *lifecycleLLM) *Handler {
	t.Helper()
	svc, err := application.NewService(application.Deps{Store: &stubStore{}, LLM: llm})
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(svc, Timeouts{StreamTotal: time.Hour, Heartbeat: time.Millisecond})
}

func lifecycleContext(w http.ResponseWriter) *gin.Context {
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/chat/stream", strings.NewReader(`{"message":"hello"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	ginc.SetUserID(c, 7)
	return c
}

type blockedHeartbeatWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
	once             sync.Once
}

func (w *blockedHeartbeatWriter) Flush() {
	w.once.Do(func() { close(w.entered); <-w.release })
	w.ResponseRecorder.Flush()
}

func TestStreamPanicWaitsForHeartbeatExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writer := &blockedHeartbeatWriter{
			ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{}),
		}
		h := lifecycleHandler(t, &lifecycleLLM{stream: func(context.Context) (application.Reply, error) {
			<-writer.entered
			panic("upstream panic")
		}})
		recovered := make(chan any, 1)
		go func() {
			defer func() { recovered <- recover() }()
			h.ChatStream(lifecycleContext(writer))
		}()
		<-writer.entered
		synctest.Wait()
		var early bool
		select {
		case <-recovered:
			early = true
		default:
		}
		close(writer.release)
		synctest.Wait()
		if early {
			t.Fatal("panic reached recovery while heartbeat still held the response writer")
		}
		if got := <-recovered; got != "upstream panic" {
			t.Fatalf("panic was lost: %v", got)
		}
	})
}

type failedHeartbeatWriter struct {
	header http.Header
	failed chan struct{}
	once   sync.Once
}

func (w *failedHeartbeatWriter) Header() http.Header { return w.header }
func (*failedHeartbeatWriter) WriteHeader(int)       {}
func (*failedHeartbeatWriter) Flush()                {}
func (w *failedHeartbeatWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.failed) })
	return 0, io.ErrClosedPipe
}

func TestHeartbeatFailureCancelsUpstream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writer := &failedHeartbeatWriter{header: make(http.Header), failed: make(chan struct{})}
		h := lifecycleHandler(t, &lifecycleLLM{stream: func(ctx context.Context) (application.Reply, error) {
			<-ctx.Done()
			return application.Reply{}, ctx.Err()
		}})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := lifecycleContext(writer)
		c.Request = c.Request.WithContext(ctx)
		done := make(chan struct{})
		go func() { defer close(done); h.ChatStream(c) }()
		<-writer.failed
		synctest.Wait()
		var finished bool
		select {
		case <-done:
			finished = true
		default:
		}
		cancel()
		<-done
		if !finished {
			t.Fatal("failed heartbeat left the upstream call running")
		}
	})
}
