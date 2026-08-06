package bootstrap

import (
	"fmt"
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
	// JWT issuer 不在此处创建：改由 NewRuntime 创建一次并持有，避免"预检创建一份又丢弃、
	// 随后再创建一份"的冗余（见 runtime.go）。
	if err := initVerify(); err != nil {
		return fmt.Errorf("init validator: %w", err)
	}
	checkJSONRuntime()

	applog.InitAccessLog(cfg)

	// 个人信息无保留期：配置注释承诺过会提示一次，此前只是承诺。
	for _, w := range cfg.RetentionWarnings() {
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

// closeProcessRuntime 收尾进程级设施（刷日志缓冲）。
//
// 它**不关闭任何连接**：连接由 Resources.Close 按建立的逆序释放。
func closeProcessRuntime() {
	applog.SyncAccessLog()
	logger.Sync()
}
