package logging

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
)

func newConsoleLogger() *logger.Logger {
	return logger.MustNew(
		logger.WithConsole(true),
		logger.WithFile(false),
		logger.WithReplaceGlobals(false),
	)
}

func TestOpenWithConfigSetsDefaultLogger(t *testing.T) {
	built := newConsoleLogger()
	var gotOpts int
	factory := Factory(func(opts ...logger.Option) (*logger.Logger, error) {
		gotOpts = len(opts)
		return built, nil
	})

	if err := openForTest(t, factory, &config.Config{
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
	// 纯日志基础 8 项 + MaxAge + MaxBackups；没有通知队列。
	if gotOpts != 10 {
		t.Fatalf("options 数量 = %d, want 10（MaxAge/MaxBackups 配置后应透传）", gotOpts)
	}
}

func TestOpenWithConfigOmitsZeroRetentionOptions(t *testing.T) {
	var gotOpts int
	factory := Factory(func(opts ...logger.Option) (*logger.Logger, error) {
		gotOpts = len(opts)
		return newConsoleLogger(), nil
	})

	// MaxAge/MaxBackups 为 0 时不透传（0 在库内语义为「关闭清理」，与未配置不同）
	if err := openForTest(t, factory, &config.Config{}); err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	if gotOpts != 8 {
		t.Fatalf("options 数量 = %d, want 8（零值保留策略不应透传）", gotOpts)
	}
}

// The pinned SDK rejects two disabled outputs. A failed constructor must leave
// the prior logger installed and allow a later valid initialization.
func TestOpenWithConfigPreservesResolvedSDKOutputContract(t *testing.T) {
	previous := logger.Default()
	err := openDefaultForTest(t, nil)
	if !errors.Is(err, logger.ErrNoOutput) {
		t.Fatalf("SDK output rejection lost: %v", err)
	}
	if logger.Default() != previous {
		t.Fatal("failed initialization replaced the default logger")
	}

	if err := openDefaultForTest(t, &config.Config{Log: config.LogConfig{ConsoleStdout: 1}}); err != nil {
		t.Fatalf("previous output initialization left an installed binding: %v", err)
	}
}

func TestOpenWithConfigWritesDrainsAndPreservesAlertTargets(t *testing.T) {
	type delivery struct{ target, text string }
	deliveries := make(chan delivery, 2)
	sink := &callbackNotification{warn: func(target string, args ...string) { deliveries <- delivery{target, strings.Join(args, " ")} }}
	dir := t.TempDir()
	cfg := &config.Config{Log: config.LogConfig{Path: filepath.Join(dir, "bridge"), FileStdout: 1}}
	previous := logger.Default()
	p := mustOpen(t, Options{Config: cfg, Notification: sink})
	t.Cleanup(func() {
		if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	logger.Infof("bridge main %d", 7)
	p.AccessLogger().InfoCtx(t.Context(), "bridge access")
	logger.HError(fmt.Sprintf("default alert %d", 1))
	adapter := &News{notifier: sink}
	adapter.SendTo("explicit", "explicit alert 2")
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if logger.Default() != previous {
		t.Fatal("drain did not restore the previous default logger")
	}
	got := make(map[string]string)
	for range 2 {
		d := <-deliveries
		got[d.target] = d.text
	}
	if got[""] != "default alert 1" || got["explicit"] != "explicit alert 2" {
		t.Fatalf("callbacks=%v", got)
	}
	logs := readLogs(t, dir)
	if !strings.Contains(logs, "bridge main 7") || !strings.Contains(logs, "bridge access") {
		t.Fatalf("logs=%s", logs)
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

func openForTest(t *testing.T, factory Factory, cfg *config.Config) error {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	p, err := Open(t.Context(), Options{Config: cfg, MainFactory: factory, AccessFactory: func(...logger.Option) (*logger.Logger, error) { return newConsoleLogger(), nil }})
	if p != nil {
		t.Cleanup(func() {
			if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Error(err)
			}
		})
	}
	return err
}
func openDefaultForTest(t *testing.T, cfg *config.Config) error {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	p, err := Open(t.Context(), Options{Config: cfg})
	if p != nil {
		t.Cleanup(func() {
			if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Error(err)
			}
		})
	}
	return err
}
