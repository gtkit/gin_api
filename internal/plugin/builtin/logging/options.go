package logging

import (
	"fmt"
	"strings"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"
	sdk "github.com/gtkit/logger/v2"
)

const (
	defaultLogPath       = "./logs/app_client"
	defaultAccessLogPath = "./logs/access"
	alertQueueLimit      = 64
)

func mainOptions(cfg *config.Config) ([]sdk.Option, error) {
	notifier, err := news.NewWithConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("init alert notifier: %w", err)
	}

	opts := []sdk.Option{
		sdk.WithPath(resolvePath(cfg.Log.Path, defaultLogPath)),
		// 相对路径锚定到稳定写根，日志落盘位置不依赖进程 cwd（锚定机制在库内）。
		sdk.WithBasePath(resource.WriteRoot()),
		sdk.WithLevel(strings.TrimSpace(cfg.Log.Level)), // 空值由库回退默认 info
		sdk.WithConsole(cfg.Log.ConsoleStdout != 0),
		sdk.WithFile(cfg.Log.FileStdout != 0),
		sdk.WithDivision("daily"),
		sdk.WithMaxSize(cfg.Log.MaxSize), // 0 由库回退默认
		sdk.WithCompress(cfg.Log.Compress != 0),
		sdk.WithMessager(&News{notifier: notifier}),
		sdk.WithMessagerQueueSize(alertQueueLimit),
	}
	// MaxAge/MaxBackups 的 0 在库内语义为「关闭清理、永久保留」，与「未配置」不同；
	// 未配置时不透传，保留库默认的保留策略。
	if cfg.Log.MaxAge > 0 {
		opts = append(opts, sdk.WithMaxAge(cfg.Log.MaxAge))
	}
	if cfg.Log.MaxBackups > 0 {
		opts = append(opts, sdk.WithMaxBackups(cfg.Log.MaxBackups))
	}

	return opts, nil
}

func accessOptions(cfg *config.Config) []sdk.Option {
	path := defaultAccessLogPath
	if value := strings.TrimSpace(cfg.Log.Path); value != "" {
		path = value + "_access"
	}
	return []sdk.Option{
		sdk.WithPath(path), sdk.WithBasePath(resource.WriteRoot()),
		sdk.WithLevel(strings.TrimSpace(cfg.Log.Level)), sdk.WithConsole(cfg.Log.ConsoleStdout != 0),
		sdk.WithFile(cfg.Log.FileStdout != 0), sdk.WithDivision("daily"),
		sdk.WithBuffered(true), sdk.WithReplaceGlobals(false),
	}
}

// resolvePath 空白路径回退到默认值；锚定交给库的 WithBasePath。
func resolvePath(path, fallback string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return fallback
	}

	return path
}

// News 把 news.Notifier 适配成 loggerv2 的 Messager。
type News struct {
	notifier *news.Notifier
}

func (n *News) Send(msg string)        { n.notifier.Warn("", msg) }
func (n *News) SendTo(url, msg string) { n.notifier.Warn(url, msg) }
