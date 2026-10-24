package logging

import (
	"strings"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"
	"github.com/gtkit/logger/v2"
)

const (
	defaultLogPath       = "./logs/app_client"
	defaultAccessLogPath = "./logs/access"
	alertQueueLimit      = 64
)

func mainOptions(cfg *config.Config, notifier news.AlertSink) []logger.Option {
	opts := []logger.Option{
		logger.WithPath(resolvePath(cfg.Log.Path, defaultLogPath)),
		// 相对路径锚定到稳定写根，日志落盘位置不依赖进程 cwd（锚定机制在库内）。
		logger.WithBasePath(resource.WriteRoot()),
		logger.WithLevel(strings.TrimSpace(cfg.Log.Level)), // 空值由库回退默认 info
		logger.WithConsole(cfg.Log.ConsoleStdout != 0),
		logger.WithFile(cfg.Log.FileStdout != 0),
	}
	opts = append(opts, fileOptions(cfg.Log)...)
	if notifier != nil {
		opts = append(opts, logger.WithMessager(&News{notifier: notifier}), logger.WithMessagerQueueSize(alertQueueLimit))
	}
	return opts
}

// fileOptions shares rotation and retention between main and access logs.
func fileOptions(cfg config.LogConfig) []logger.Option {
	opts := []logger.Option{
		logger.WithDivision("both"),
		logger.WithMaxSize(cfg.MaxSize), // 0 retains the SDK default.
		logger.WithCompress(cfg.Compress != 0),
	}
	// Zero means unspecified here, not the SDK's unlimited retention.
	if cfg.MaxAge > 0 {
		opts = append(opts, logger.WithMaxAge(cfg.MaxAge))
	}
	if cfg.MaxBackups > 0 {
		opts = append(opts, logger.WithMaxBackups(cfg.MaxBackups))
	}

	return opts
}

func accessOptions(cfg *config.Config) []logger.Option {
	path := defaultAccessLogPath
	if value := strings.TrimSpace(cfg.Log.Path); value != "" {
		path = value + "_access"
	}
	opts := []logger.Option{
		logger.WithPath(path), logger.WithBasePath(resource.WriteRoot()),
		logger.WithLevel(strings.TrimSpace(cfg.Log.Level)), logger.WithConsole(cfg.Log.ConsoleStdout != 0),
		logger.WithFile(cfg.Log.FileStdout != 0),
		logger.WithBuffered(true), logger.WithReplaceGlobals(false),
	}
	return append(opts, fileOptions(cfg.Log)...)
}

// resolvePath 空白路径回退到默认值；锚定交给库的 WithBasePath。
func resolvePath(path, fallback string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return fallback
	}

	return path
}

// News forwards SDK callbacks to a borrowed alert sink, including explicit URLs.
type News struct {
	notifier news.AlertSink
}

func (n *News) Send(msg string)        { n.notifier.Warn("", msg) }
func (n *News) SendTo(url, msg string) { n.notifier.Warn(url, msg) }
