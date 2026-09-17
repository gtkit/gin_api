package bootstrap

import (
	"fmt"
	"os"
	"strings"
	"time"

	jwtauth "github.com/gtkit/gin-api/internal/pkg/jwtauth"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/json"
	logger "github.com/gtkit/logger/v2"
	"github.com/gtkit/verify"
	"go.uber.org/zap"
)

// 进程级设施的测试缝：单元测试以桩替换，避免真实初始化日志/JWT/校验器。
var (
	initLogWithConfig = applog.InitWithConfig
	initJWTWithConfig = jwtauth.Assemble
	initVerify        = verify.New
	checkJSONRuntime  = json.CheckJSON
	// drainProcessLogs 是日志排空的测试缝：装配失败路径"先刷写再返回"的
	// 行为必须可被断言，否则回归时没有任何信号。
	drainProcessLogs = drainLogsWithBudget
)

// initProcessRuntime 初始化**进程级、无连接**的运行时设施：时区、日志、校验器、JSON 实现。
//
// 数据库与 Redis 连接不在这里建立：它们是有生命周期的资源，由 Resources 统一持有并逆序关闭。
// 旧实现把两者混在一起，于是"初始化"既包含无需清理的全局设置，又包含必须清理的连接，
// 失败路径要清理哪些就没有确定答案。
func initProcessRuntime(cfg *config.Config) error {
	applyRuntimeTimezone(cfg)
	if err := initLogWithConfig(cfg); err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	installGinDiagnostics()
	// 主 logger 就绪之后的任何失败都要先刷写再返回（resource-lifecycle spec）：
	// 它持有异步告警队列，装配失败恰是最需要告警送达的时刻——不刷写的话，
	// 启动失败的原因既不落盘也不告警。
	// JWT issuer 不在此处创建：改由 NewRuntime 创建一次并持有，避免"预检创建一份又丢弃、
	// 随后再创建一份"的冗余（见 runtime.go）。
	if err := initVerify(); err != nil {
		drainProcessLogs(loggerDrainBudget, nil)
		return fmt.Errorf("init validator: %w", err)
	}
	checkJSONRuntime()

	if err := applog.InitAccessLog(cfg); err != nil {
		drainProcessLogs(loggerDrainBudget, nil)
		return fmt.Errorf("init access logger: %w", err)
	}

	// 一家 LLM 厂商都没配：AI 端点整个不注册，而那与"路由写错了"从外部看一样。
	for _, w := range cfg.AIWarnings() {
		logger.Warn(w)
	}

	for _, w := range cfg.Proxy.Warnings(cfg.Env) {
		logger.Warn(w)
	}

	logger.Infof("runtime initialized with env=%s", cfg.Env)
	return nil
}

// logWriter 把引擎的行式诊断输出转发到结构化日志器。
//
// io.Writer 契约要求报告写入字节数，这里恒返回 len(p)：转发是尽力而为，
// 让引擎因日志出口报错而中断自身处理没有任何收益。
type logWriter struct {
	logf func(format string, args ...any)
}

func (w logWriter) Write(p []byte) (int, error) {
	if msg := strings.TrimSpace(string(p)); msg != "" {
		w.logf("%s", msg)
	}
	return len(p), nil
}

// installGinDiagnostics 把 gin 的包级诊断输出接管到结构化日志器。
//
// gin.DefaultWriter / gin.DefaultErrorWriter 是包级全局且无并发保护，
// 只允许在进程装配阶段安装一次，进入请求并发阶段后不再改动。
func installGinDiagnostics() {
	gin.DefaultWriter = logWriter{logf: logger.Infof}
	gin.DefaultErrorWriter = logWriter{logf: logger.Warnf}
}

// resolveDBCredentials 决定本次装配的数据库连接使用哪套凭据。
//
// 只有 migrate 子命令会请求迁移凭据；server 恒走运行凭据。迁移凭据未配置时回退运行凭据
// 并打印告警——不失败，因为存量部署尚未配置迁移凭据，直接 fail 会破坏它们。
// 告警只说明"正在使用运行凭据"，绝不带用户名或密码。
func resolveDBCredentials(cfg *config.Config, opts Options) config.DBCredentials {
	if !opts.UseMigrationCredentials {
		return cfg.Database.RuntimeCredentials()
	}
	if creds, ok := cfg.Database.MigrationCredentials(); ok {
		return creds
	}
	logger.Warn(migrationFallbackWarning)
	return cfg.Database.RuntimeCredentials()
}

// migrationFallbackWarning 是回退告警的固定文本。
//
// 刻意做成无格式化占位符的常量：告警绝不能带用户名或密码，而一旦允许拼接，
// 就迟早有人为了"方便排障"把凭据加进来。测试锁住这一点。
const migrationFallbackWarning = "migration is using the runtime database credentials: " +
	"set database.migrate_username to grant DDL only to a dedicated account"

func applyRuntimeTimezone(cfg *config.Config) {
	if cfg == nil || strings.TrimSpace(cfg.App.Timezone) == "" {
		return
	}
	loc, err := time.LoadLocation(cfg.App.Timezone)
	if err != nil {
		logger.Warn("load runtime timezone failed", zap.String("timezone", cfg.App.Timezone), zap.Error(err))
		return
	}
	// 在所有 goroutine 启动之前设置，后续应通过显式 *time.Location 传递。
	time.Local = loc //nolint:gosmopolitan // Process-wide timezone is set once before any worker goroutine starts.
}

// loggerDrainBudget 是进程结束时主 logger 刷写与告警队列排空的总预算。
//
// 单次发送的超时只约束一条消息；队列内多条消息乘以多通道串行发送，
// 无总预算的最坏排空时间以十分钟计——关停被告警通道拖住，本身就成了
// 下一个告警级故障。主实例不缓冲文件写（错误日志不因缓冲丢失），
// 预算截断牺牲的只是尚未送出的告警通知。
const loggerDrainBudget = 30 * time.Second

// closeProcessRuntime 收尾进程级设施（刷日志缓冲）。
//
// 它**不关闭任何连接**：连接由 Resources.Close 按建立的逆序释放。
// interrupt 是强制关停信号（可为 nil）：第二个信号到达时放弃剩余排空——
// 契约是"取消各阶段剩余等待"，而日志排空正是进程的最后一段等待。
func closeProcessRuntime(interrupt <-chan struct{}) {
	drainProcessLogs(loggerDrainBudget, interrupt)
}

// drainLogsWithBudget 在**同一预算**内完成 access log 与主 logger（含告警队列）
// 的排空；预算耗尽或强制信号到达即放弃剩余排空。
//
// access log 的 Sync 与主 logger 同在预算 goroutine 内：它此前不受任何预算
// 约束，一个写不动的日志出口能把进程退出无限期扣住。
// 被放弃的排空协程随进程退出回收。说明只能走 stderr：此刻日志设施
// 自身正在关闭，它已经不是可用的输出通道。
func drainLogsWithBudget(budget time.Duration, interrupt <-chan struct{}) {
	done := make(chan struct{})
	go func() {
		applog.SyncAccessLog()
		logger.Sync()
		close(done)
	}()

	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
	case <-interrupt: // nil 通道永远阻塞：未安装信号时本分支自然失效
		fmt.Fprintln(os.Stderr, "forced shutdown: abandoning the remaining log drain")
	case <-timer.C:
		fmt.Fprintf(os.Stderr,
			"logger drain exceeded its %s budget; abandoning the remaining alert notifications\n", budget)
	}
}
