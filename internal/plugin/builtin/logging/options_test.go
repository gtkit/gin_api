package logging

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtkit/logger/v2"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
)

func newConsoleLogger() *logger.Logger {
	return logger.MustNew(
		logger.WithConsole(true),
		logger.WithFile(false),
		logger.WithReplaceGlobals(false),
	)
}

func TestInitWithConfigSetsDefaultLogger(t *testing.T) {
	built := newConsoleLogger()
	var gotOpts int
	factory := Factory(func(opts ...logger.Option) (*logger.Logger, error) {
		gotOpts = len(opts)
		return built, nil
	})

	if err := initForTest(t, factory, &config.Config{
		Log: config.LogConfig{
			Path:          "logs/app",
			Level:         "warn",
			ConsoleStdout: 1,
			MaxAge:        30,
			MaxBackups:    7,
		},
	}); err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}

	if logger.Default() != built {
		t.Fatal("InitWithConfig 应把构建的实例设为 loggerv2 默认实例")
	}
	// 基础 10 项（含告警队列上限）+ MaxAge + MaxBackups
	if gotOpts != 12 {
		t.Fatalf("options 数量 = %d, want 12（MaxAge/MaxBackups 配置后应透传）", gotOpts)
	}
}

func TestInitWithConfigOmitsZeroRetentionOptions(t *testing.T) {
	var gotOpts int
	factory := Factory(func(opts ...logger.Option) (*logger.Logger, error) {
		gotOpts = len(opts)
		return newConsoleLogger(), nil
	})

	// MaxAge/MaxBackups 为 0 时不透传（0 在库内语义为「关闭清理」，与未配置不同）
	if err := initForTest(t, factory, &config.Config{}); err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	if gotOpts != 10 {
		t.Fatalf("options 数量 = %d, want 10（零值保留策略不应透传）", gotOpts)
	}
}

// The locked SDK and the local workspace SDK differ for two disabled outputs:
// the former falls back to stdout, the latter rejects it. The adapter preserves
// the resolved dependency's contract without referring to newer SDK symbols.
func TestInitWithConfigPreservesResolvedSDKOutputContract(t *testing.T) {
	probe, sdkErr := logger.New(logger.WithConsole(false), logger.WithFile(false), logger.WithReplaceGlobals(false))
	if probe != nil {
		probe.Sync()
	}
	previous := logger.Default()
	err := initDefaultForTest(t, nil)
	if sdkErr != nil {
		if err == nil || !strings.Contains(err.Error(), sdkErr.Error()) {
			t.Fatalf("SDK output error lost: got %v, SDK %v", err, sdkErr)
		}
		if logger.Default() != previous {
			t.Fatal("failed initialization replaced the default logger")
		}
	} else {
		if err != nil {
			t.Fatalf("legacy SDK output fallback changed: %v", err)
		}
		if logger.Default() == previous {
			t.Fatal("successful legacy fallback did not publish its owner")
		}
		if err := applog.Drain(t.Context()); err != nil {
			t.Fatal(err)
		}
		if logger.Default() != previous {
			t.Fatal("legacy output owner was not released")
		}
	}
	t.Cleanup(func() {
		if err := applog.Drain(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := initDefaultForTest(t, &config.Config{Log: config.LogConfig{ConsoleStdout: 1}}); err != nil {
		t.Fatalf("previous output initialization left an installed binding: %v", err)
	}
}

func TestInitWithConfigWritesDrainsAndPreservesAlertTargets(t *testing.T) {
	testutil.RequireLoopback(t)

	type delivery struct{ path, text string }
	deliveries := make(chan delivery, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Type    string `json:"msg_type"`
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode alert: %v", err)
		}
		if r.Method != http.MethodPost || body.Type != "text" {
			t.Errorf("unexpected alert request: %s, %q", r.Method, body.Type)
		}
		select {
		case deliveries <- delivery{r.URL.Path, body.Content.Text}:
		default:
			t.Error("unexpected duplicate alert delivery")
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"code":0,"msg":"success"}`); err != nil {
			t.Errorf("test response I/O: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	cfg := &config.Config{
		Log:  config.LogConfig{Path: filepath.Join(dir, "bridge"), FileStdout: 1},
		News: config.NewsConfig{FSURL: server.URL + "/default"},
	}
	previous := logger.Default()
	t.Cleanup(func() {
		if err := applog.Drain(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := initDefaultForTest(t, cfg); err != nil {
		t.Fatal(err)
	}
	if logger.Default() == previous {
		t.Fatal("compatibility entry point did not install its owner")
	}
	applog.Infof("bridge main %d", 7)
	applog.Log(t.Context(), applog.LogEntry{Level: applog.LevelInfo, Stream: applog.AccessStream, Message: "bridge access"})
	applog.HErrorf("default alert %d", 1)
	notifier, err := news.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &News{notifier: notifier}
	adapter.SendTo(server.URL+"/explicit", "explicit alert 2")
	if err := applog.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if logger.Default() != previous {
		t.Fatal("drain did not restore the previous default logger")
	}
	if err := applog.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for range 2 {
		select {
		case sent := <-deliveries:
			got[sent.path] = sent.text
		default:
			t.Fatal("drain returned before both alert deliveries completed")
		}
	}
	for path, message := range map[string]string{"/default": "default alert 1", "/explicit": "explicit alert 2"} {
		if !strings.Contains(got[path], message) {
			t.Errorf("alert %s = %q, want %q", path, got[path], message)
		}
	}
	logs := readLogs(t, dir)
	for _, message := range []string{"bridge main 7", "bridge access", "default alert 1"} {
		if !strings.Contains(logs, message) {
			t.Errorf("missing %q in drained logs: %s", message, logs)
		}
	}
}

func TestResolvePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		fallback string
		want     string
	}{
		{name: "空白回退默认", path: "   ", fallback: "./logs/app_client", want: "./logs/app_client"},
		{name: "非空原样", path: "logs/app", fallback: "./logs/app_client", want: "logs/app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolvePath(tt.path, tt.fallback); got != tt.want {
				t.Errorf("resolvePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func initForTest(t *testing.T, factory Factory, cfg *config.Config) error {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	p := New(Options{Configuration: fixedConfiguration{cfg}, MainFactory: factory, AccessFactory: func(...logger.Option) (*logger.Logger, error) { return newConsoleLogger(), nil }})
	t.Cleanup(func() {
		if err := p.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return p.Init(t.Context())
}

type fixedConfiguration struct{ cfg *config.Config }

func (f fixedConfiguration) Value() (*config.Config, error) { return f.cfg, nil }
func initDefaultForTest(t *testing.T, cfg *config.Config) error {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	p := New(Options{Configuration: fixedConfiguration{cfg}})
	t.Cleanup(func() {
		if err := p.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return p.Init(t.Context())
}
