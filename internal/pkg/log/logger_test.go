package log

import (
	"testing"

	loggerv2 "github.com/gtkit/logger/v2"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func newConsoleLogger() *loggerv2.Logger {
	return loggerv2.MustNew(
		loggerv2.WithConsole(true),
		loggerv2.WithFile(false),
		loggerv2.WithReplaceGlobals(false),
	)
}

func TestInitWithConfigSetsDefaultLogger(t *testing.T) {
	resetLogHooksForTest()

	built := newConsoleLogger()
	var gotOpts int
	setNewLoggerForTest(func(opts ...loggerv2.Option) (*loggerv2.Logger, error) {
		gotOpts = len(opts)
		return built, nil
	})

	if err := InitWithConfig(&config.Config{
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

	if loggerv2.Default() != built {
		t.Fatal("InitWithConfig 应把构建的实例设为 loggerv2 默认实例")
	}
	// 基础 10 项（含告警队列上限）+ MaxAge + MaxBackups
	if gotOpts != 12 {
		t.Fatalf("options 数量 = %d, want 12（MaxAge/MaxBackups 配置后应透传）", gotOpts)
	}
}

func TestInitWithConfigOmitsZeroRetentionOptions(t *testing.T) {
	resetLogHooksForTest()

	var gotOpts int
	setNewLoggerForTest(func(opts ...loggerv2.Option) (*loggerv2.Logger, error) {
		gotOpts = len(opts)
		return newConsoleLogger(), nil
	})

	// MaxAge/MaxBackups 为 0 时不透传（0 在库内语义为「关闭清理」，与未配置不同）
	if err := InitWithConfig(&config.Config{}); err != nil {
		t.Fatalf("InitWithConfig: %v", err)
	}
	if gotOpts != 10 {
		t.Fatalf("options 数量 = %d, want 10（零值保留策略不应透传）", gotOpts)
	}
}

func TestInitWithConfigNilConfigDoesNotPanic(t *testing.T) {
	resetLogHooksForTest()
	setNewLoggerForTest(func(...loggerv2.Option) (*loggerv2.Logger, error) { return newConsoleLogger(), nil })

	if err := InitWithConfig(nil); err != nil {
		t.Fatalf("InitWithConfig(nil): %v", err)
	}
	Init()
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
