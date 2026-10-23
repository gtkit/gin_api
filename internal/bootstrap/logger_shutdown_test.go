package bootstrap

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/logging"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/gtkit/logger/v2"
)

func TestHTTPProducersKeepNativeLogsUntilHandlersReturn(t *testing.T) {
	testutil.RequireLoopback(t)
	for _, mode := range []string{"timeout", "forced", "serve exited"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			owner, err := logging.Open(t.Context(), logging.Options{Config: &config.Config{
				Log: config.LogConfig{Path: filepath.Join(dir, "main"), FileStdout: 1},
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := owner.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
			})
			main, access := logger.Default(), owner.AccessLogger()
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				defer close(returned)
				close(entered)
				<-release
				logger.Info("handler main after flush")
				access.Info("handler access after flush")
			}))
			t.Cleanup(func() { unblock(); server.Close() })
			requestDone := make(chan struct{})
			go func() {
				defer close(requestDone)
				response, requestErr := server.Client().Get(server.URL)
				if requestErr == nil {
					if _, err := io.Copy(io.Discard, response.Body); err != nil {
						t.Errorf("read response body: %v", err)
					}
					if err := response.Body.Close(); err != nil {
						t.Errorf("close response body: %v", err)
					}
				}
			}()
			waitClosed(t, entered, "handler did not start")
			manager, err := NewWorkerManager(DefaultWorkerBudget())
			if err != nil {
				t.Fatal(err)
			}
			app := &App{
				runtime:    &Runtime{owners: &runtimeResources{logs: &processLogs{logger: owner}}, drain: owner.Drain},
				httpServer: server.Config, httpConfig: HTTPConfig{ShutdownTimeout: 20 * time.Millisecond},
				httpStarted: true, workers: manager, shutdown: NewShutdownCoordinator(),
			}
			switch mode {
			case "forced":
				app.shutdown.Force()
				if err := app.shutdownHTTP(t.Context()); err != nil {
					t.Fatal(err)
				}
			case "serve exited":
				if err := server.Listener.Close(); err != nil {
					t.Fatal(err)
				}
				if err := app.gracefulShutdown(t.Context(), nil, true, nil); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("serve exit skipped handler drain: %v", err)
				}
			default:
				if err := app.shutdownHTTP(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("shutdown=%v", err)
				}
			}
			if app.httpDrained {
				t.Fatal("live handler reported drained")
			}
			if err := app.closeResources(t.Context()); err == nil {
				t.Fatal("unconfirmed producer did not prevent resource close")
			}
			if logger.Default() != main {
				t.Fatal("abnormal close replaced the live main logger")
			}
			unblock()
			waitClosed(t, returned, "handler did not return")
			waitClosed(t, requestDone, "HTTP client did not finish")
			if err := owner.Drain(t.Context()); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				contents, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := output.Write(contents); err != nil {
					t.Fatal(err)
				}
			}
			for _, message := range []string{"handler main after flush", "handler access after flush"} {
				if !strings.Contains(output.String(), message) {
					t.Fatalf("late native log lost: %q", message)
				}
			}
			// A later successful shutdown, including extension cleanup, permits Close.
			if err := server.Config.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			app.httpDrained = true
			if err := app.closeExtensions(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := app.closeResources(t.Context()); err != nil {
				t.Fatal(err)
			}
			if logger.Default() == main {
				t.Fatal("later safe close did not restore the default")
			}
		})
	}
}
