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
		logger.WithDivision("daily"),
		logger.WithMaxSize(cfg.Log.MaxSize), // 0 由库回退默认
		logger.WithCompress(cfg.Log.Compress != 0),
	}
	if notifier != nil {
		opts = append(opts, logger.WithMessager(&News{notifier: notifier}), logger.WithMessagerQueueSize(alertQueueLimit))
	}
	// MaxAge/MaxBackups 的 0 在库内语义为「关闭清理、永久保留」，与「未配置」不同；
	// 未配置时不透传，保留库默认的保留策略。
	if cfg.Log.MaxAge > 0 {
		opts = append(opts, logger.WithMaxAge(cfg.Log.MaxAge))
	}
	if cfg.Log.MaxBackups > 0 {
		opts = append(opts, logger.WithMaxBackups(cfg.Log.MaxBackups))
	}

	return opts
}

func accessOptions(cfg *config.Config) []logger.Option {
	path := defaultAccessLogPath
	if value := strings.TrimSpace(cfg.Log.Path); value != "" {
		path = value + "_access"
	}
	return []logger.Option{
		logger.WithPath(path), logger.WithBasePath(resource.WriteRoot()),
		logger.WithLevel(strings.TrimSpace(cfg.Log.Level)), logger.WithConsole(cfg.Log.ConsoleStdout != 0),
		logger.WithFile(cfg.Log.FileStdout != 0), logger.WithDivision("daily"),
		logger.WithBuffered(true), logger.WithReplaceGlobals(false),
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

// News forwards SDK callbacks to a borrowed alert sink, including explicit URLs.
type News struct {
	notifier news.AlertSink
}

func (n *News) Send(msg string)        { n.notifier.Warn("", msg) }
func (n *News) SendTo(url, msg string) { n.notifier.Warn(url, msg) }
