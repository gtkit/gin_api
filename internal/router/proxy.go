package router

import (
	logger "github.com/gtkit/logger/v2"

	"github.com/gin-gonic/gin"
)

// applyTrustedProxies 设置可信代理边界与允许采信的转发头。
//
// 语义要点（Gin 的默认值在这里是不安全的，必须显式覆盖）：
//
//   - Gin 默认 ForwardedByClientIP=true 且可信代理集合为 0.0.0.0/0，即信任所有代理，
//     c.ClientIP() 直接采信客户端完全可控的 X-Forwarded-For / X-Real-IP；
//   - "不调用 SetTrustedProxies" 与 "传空列表" 语义完全不同：前者沿用不安全默认，
//     后者才是"谁都不信任"。因此空配置也要显式调用。
//
// 明确不使用 TrustedPlatform：Gin 对它指定的 header 直接采信、不校验来源代理。
// 在自建反向代理上启用它，只是把伪造面从一个 header 换到另一个。它唯一成立的前提是
// 托管平台保证该 header 不可被客户端注入，本母版不预设部署在这类平台上。
func applyTrustedProxies(engine *gin.Engine, opt RouterOptions) {
	// RemoteIPHeaders 必须先于 SetTrustedProxies 设定：它决定"可信代理转发时看哪些头"。
	// 显式赋值（含空切片）以覆盖 Gin 的默认 ["X-Forwarded-For", "X-Real-IP"]——
	// 未在配置里列出的头一律不参与取值。
	engine.RemoteIPHeaders = append([]string(nil), opt.RemoteIPHeaders...)

	if err := engine.SetTrustedProxies(opt.TrustedProxies); err != nil {
		// 走不到这里：条目已在 config.Validate 里按同一口径（CIDR 或单 IP）校验过。
		// 万一走到，唯一安全的降级方向是"谁都不信任"——绝不能留着 Gin 的默认值，
		// 那等于信任所有代理。同时必须响亮报错，避免运维以为配置已生效。
		logger.Errorf("invalid trusted proxy configuration, falling back to trusting no proxy at all: %v", err)
		if fallbackErr := engine.SetTrustedProxies(nil); fallbackErr != nil {
			logger.Errorf("failed to fall back to an empty trusted proxy set: %v", fallbackErr)
		}
	}
}
