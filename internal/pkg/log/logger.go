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
	"sync"

	loggerv2 "github.com/gtkit/logger/v2"

	"github.com/gtkit/gin-api/internal/pkg/alertbot"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"
)

const (
	// defaultLogPath 仅作零配置兜底（config.Validate 已强制 log.path 必填）。
	defaultLogPath       = "./logs/app_client"
	defaultAccessLogPath = "./logs/access"

	// alertQueueLimit 是异步告警队列的深度上限（库默认 1024，此处收紧）。
	// 告警只承载进程级事件，积压几十条就已经是告警风暴——继续排队的每一条
	// 都在拉长关停时的排空时间（每条最坏 = 各通道超时之和），丢弃并计数
	//（库内 DroppedMessages 可观测）比无界排队诚实。
	alertQueueLimit = 64
)

// newLogger 为测试注入点，默认走 loggerv2.New。
//
// New 而不是 MustNew：日志配置错误是启动失败，不是 panic——
// panic 会绕过装配层的错误聚合与回滚链。
var newLogger = loggerv2.New

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
		loggerv2.WithMessager(buildMessager(cfg, notifier)),
		loggerv2.WithMessagerQueueSize(alertQueueLimit),
	}
	// MaxAge/MaxBackups 的 0 在库内语义为「关闭清理、永久保留」，与「未配置」不同；
	// 未配置时不透传，保留库默认的保留策略。
	if cfg.Log.MaxAge > 0 {
		opts = append(opts, loggerv2.WithMaxAge(cfg.Log.MaxAge))
	}
	if cfg.Log.MaxBackups > 0 {
		opts = append(opts, loggerv2.WithMaxBackups(cfg.Log.MaxBackups))
	}

	inst, err := newLogger(opts...)
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}
	loggerv2.SetDefault(inst)
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

// buildMessager 组装告警通道。
//
// 群机器人未启用时只有 news 一条通道，形态与从前完全一致。启用时两条**并存**
// 而不是替代：静默改变"告警发到哪里"，会让某个值班群在毫无察觉的情况下
// 不再收到消息——那种失效只能靠"怎么一直没人反馈"来发现。
//
// 机器人构造失败时**不让启动失败**，只降级为不接这条通道：它是既有告警之外
// 的增量，为它把整个进程挡在门外，代价与收益不成比例。news 通道构造失败
// 仍然是启动失败（见 InitWithConfig），因为那是原有的唯一通道。
func buildMessager(cfg *config.Config, notifier *news.Notifier) loggerv2.Messager {
	base := &News{notifier: notifier}

	bot, err := alertbot.New(cfg.AlertBot)
	if err != nil || bot == nil {
		return base
	}
	return &fanoutMessager{channels: []loggerv2.Messager{base, bot}}
}

// fanoutMessager 把一条告警发往全部通道。
//
// 逐个同步发送：Messager 没有返回值，无法上报"某条通道失败"，因此并发发送
// 换不来任何可观测性，只会让告警队列里多出一批无人回收的 goroutine。
// 每条通道自己带超时，总耗时因此有界。
type fanoutMessager struct {
	channels []loggerv2.Messager
}

func (f *fanoutMessager) Send(msg string) {
	for _, ch := range f.channels {
		ch.Send(msg)
	}
}

func (f *fanoutMessager) SendTo(url, msg string) {
	for _, ch := range f.channels {
		ch.SendTo(url, msg)
	}
}

// News 把 news.Notifier 适配成 loggerv2 的 Messager。
type News struct {
	notifier *news.Notifier
}

func (n *News) Send(msg string)        { n.notifier.Warn("", msg) }
func (n *News) SendTo(url, msg string) { n.notifier.Warn(url, msg) }

func resetLogHooksForTest() {
	newLogger = loggerv2.New
}

func setNewLoggerForTest(fn func(opts ...loggerv2.Option) (*loggerv2.Logger, error)) {
	newLogger = fn
}

// ---------------------------------------------------------------------------
// Access Logger — 使用 BufferedWriteSyncer，仅供 HTTP 访问日志中间件使用
// ---------------------------------------------------------------------------

var accessLog *loggerv2.Logger

// InitAccessLog 初始化 access logger（BufferedWriteSyncer 模式）。
//
// 返回 error 让启动失败：日志配置错误 panic 会绕过装配层的错误聚合与回滚链。
func InitAccessLog(cfg *config.Config) error {
	if cfg == nil {
		cfg = &config.Config{}
	}

	path := defaultAccessLogPath
	if p := strings.TrimSpace(cfg.Log.Path); p != "" {
		path = p + "_access"
	}

	inst, err := loggerv2.New(
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
	if err != nil {
		return fmt.Errorf("build access logger: %w", err)
	}
	accessLog = inst
	return nil
}

// fallbackAccessLog 缓存兜底实例：每次调用都新建会让每个请求泄漏一个
// 带内部状态的实例。纯控制台配置不可能构造失败，MustNew 在此安全。
var fallbackAccessLog = sync.OnceValue(func() *loggerv2.Logger {
	return loggerv2.MustNew(
		loggerv2.WithConsole(true),
		loggerv2.WithFile(false),
		loggerv2.WithReplaceGlobals(false),
	)
})

// AccessLog 返回 access logger 实例。
// 如果未初始化，返回缓存的纯控制台、不落盘兜底实例以保证安全——
// 兜底实例若允许写文件，会以进程 cwd 为锚在任意目录刷出 logs/（曾污染 internal/router）。
func AccessLog() *loggerv2.Logger {
	if accessLog == nil {
		return fallbackAccessLog()
	}
	return accessLog
}

// SyncAccessLog 刷写 access logger 的缓冲区并关闭文件资源。
func SyncAccessLog() {
	if accessLog != nil {
		accessLog.Sync()
	}
}
