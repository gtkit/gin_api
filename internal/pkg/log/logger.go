// Package log 负责进程级日志初始化：把 app 配置翻译成 gtkit/logger/v2 options，
// 构建主实例（经 loggerv2.SetDefault 设为进程默认实例）与独立的 access 实例。
//
// 结构：主实例（不缓冲，错误日志不允许因缓冲丢失）+ access 实例（BufferedWriteSyncer，
// 高吞吐访问日志，WithReplaceGlobals(false) 构建，不抢占 zap 全局）。
// v2 的缓冲是实例级配置，无 per-channel 缓冲，故维持双实例。
//
// requestId 贯穿、相对路径锚定、caller 校准均由 loggerv2 库内建
// （ContextWithRequestID / WithBasePath / 包级函数），本包不再承载这些逻辑。
//
// 分层约定：基础设施层直接使用 loggerv2 包级函数（logger.Info 等，写入默认实例）；
// application 层禁止 import 任何 logger 包，须定义 port 由 runtime 注入。
package log

import (
	"fmt"
	"strings"

	loggerv2 "github.com/gtkit/logger/v2"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"
)

const (
	// defaultLogPath 仅作零配置兜底（config.Validate 已强制 log.path 必填）。
	defaultLogPath       = "./logs/app_client"
	defaultAccessLogPath = "./logs/access"
)

// newLogger 为测试注入点，默认走 loggerv2.MustNew。
var newLogger = loggerv2.MustNew

func Init() {
	// 空配置不启用任何告警通道，构造不可能失败。
	_ = InitWithConfig(&config.Config{})
}

// InitWithConfig 构建主 logger 并设为进程默认实例（loggerv2.SetDefault），
// 之后各层直接用 loggerv2 包级函数记录日志。仅在进程启动阶段（单 goroutine）调用一次。
// 未初始化时包级函数写入 loggerv2 的纯控制台兜底实例（库内保证不落盘、不抢占 zap 全局）。
//
// 返回 error：告警通道（邮件/短信客户端）在通知器构造中建立，
// 构造失败必须让启动失败，不得静默降级为"配置了却没有通道"。
func InitWithConfig(cfg *config.Config) error {
	if cfg == nil {
		cfg = &config.Config{}
	}

	notifier, err := news.NewWithConfig(cfg)
	if err != nil {
		return fmt.Errorf("init alert notifier: %w", err)
	}

	opts := []loggerv2.Option{
		loggerv2.WithPath(resolvePath(cfg.Log.Path, defaultLogPath)),
		// 相对路径锚定到稳定写根，日志落盘位置不依赖进程 cwd（锚定机制在库内）。
		loggerv2.WithBasePath(resource.WriteRoot()),
		loggerv2.WithLevel(strings.TrimSpace(cfg.Log.Level)), // 空值由库回退默认 info
		loggerv2.WithConsole(cfg.Log.ConsoleStdout != 0),
		loggerv2.WithFile(cfg.Log.FileStdout != 0),
		loggerv2.WithDivision("daily"),
		loggerv2.WithMaxSize(cfg.Log.MaxSize), // 0 由库回退默认
		loggerv2.WithCompress(cfg.Log.Compress != 0),
		loggerv2.WithMessager(&News{notifier: notifier}),
	}
	// MaxAge/MaxBackups 的 0 在库内语义为「关闭清理、永久保留」，与「未配置」不同；
	// 未配置时不透传，保留库默认的保留策略。
	if cfg.Log.MaxAge > 0 {
		opts = append(opts, loggerv2.WithMaxAge(cfg.Log.MaxAge))
	}
	if cfg.Log.MaxBackups > 0 {
		opts = append(opts, loggerv2.WithMaxBackups(cfg.Log.MaxBackups))
	}

	loggerv2.SetDefault(newLogger(opts...))
	return nil
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

func resetLogHooksForTest() {
	newLogger = loggerv2.MustNew
}

func setNewLoggerForTest(fn func(opts ...loggerv2.Option) *loggerv2.Logger) {
	newLogger = fn
}

// ---------------------------------------------------------------------------
// Access Logger — 使用 BufferedWriteSyncer，仅供 HTTP 访问日志中间件使用
// ---------------------------------------------------------------------------

var accessLog *loggerv2.Logger

// InitAccessLog 初始化 access logger（BufferedWriteSyncer 模式）。
func InitAccessLog(cfg *config.Config) {
	if cfg == nil {
		cfg = &config.Config{}
	}

	path := defaultAccessLogPath
	if p := strings.TrimSpace(cfg.Log.Path); p != "" {
		path = p + "_access"
	}

	accessLog = loggerv2.MustNew(
		loggerv2.WithPath(path),
		loggerv2.WithBasePath(resource.WriteRoot()),
		loggerv2.WithLevel(strings.TrimSpace(cfg.Log.Level)),
		loggerv2.WithConsole(cfg.Log.ConsoleStdout != 0),
		loggerv2.WithFile(cfg.Log.FileStdout != 0),
		loggerv2.WithDivision("daily"),
		loggerv2.WithBuffered(true),
		// 辅助实例不抢占 zap 全局（主实例经 SetDefault 持有默认地位）。
		loggerv2.WithReplaceGlobals(false),
	)
}

// AccessLog 返回 access logger 实例。
// 如果未初始化，返回一个纯控制台、不落盘的默认实例以保证安全——
// 兜底实例若允许写文件，会以进程 cwd 为锚在任意目录刷出 logs/（曾污染 internal/router）。
func AccessLog() *loggerv2.Logger {
	if accessLog == nil {
		return loggerv2.MustNew(
			loggerv2.WithConsole(true),
			loggerv2.WithFile(false),
			loggerv2.WithReplaceGlobals(false),
		)
	}
	return accessLog
}

// SyncAccessLog 刷写 access logger 的缓冲区并关闭文件资源。
func SyncAccessLog() {
	if accessLog != nil {
		accessLog.Sync()
	}
}
