package bootstrap

import (
	"fmt"
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/json"
	"github.com/gtkit/verify"
)

// 进程级设施的测试缝：单元测试以桩替换，避免真实初始化日志/JWT/校验器。
var (
	initJWTWithConfig = jwtauth.Assemble
	initVerify        = verify.New
	checkJSONRuntime  = json.CheckJSON
)

// initProcessRuntime initializes fixed process facilities after the selected
// configuration and logging plugins are ready. The registry owns rollback.
func initProcessRuntime(cfg *config.Config) error {
	installGinDiagnostics()
	if err := initVerify(); err != nil {
		return fmt.Errorf("init validator: %w", err)
	}
	checkJSONRuntime()

	// 一家 LLM 厂商都没配：AI 端点整个不注册，而那与"路由写错了"从外部看一样。
	for _, w := range cfg.AIWarnings() {
		applog.Warn(w)
	}

	for _, w := range cfg.Proxy.Warnings(cfg.Env) {
		applog.Warn(w)
	}

	applog.Infof("runtime initialized with env=%s", cfg.Env)
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
	gin.DefaultWriter = logWriter{logf: applog.Infof}
	gin.DefaultErrorWriter = logWriter{logf: applog.Warnf}
}

// ApplyRuntimeTimezone configures time.Local during configuration Init, before
// the logging plugin starts background goroutines. Call once during startup.
func ApplyRuntimeTimezone(cfg *config.Config) {
	if cfg == nil || strings.TrimSpace(cfg.App.Timezone) == "" {
		return
	}
	loc, err := time.LoadLocation(cfg.App.Timezone)
	if err != nil {
		applog.Warn("load runtime timezone failed", applog.String("timezone", cfg.App.Timezone), applog.Err(err))
		return
	}
	// 在所有 goroutine 启动之前设置，后续应通过显式 *time.Location 传递。
	time.Local = loc //nolint:gosmopolitan // Process-wide timezone is set once before any worker goroutine starts.
}
