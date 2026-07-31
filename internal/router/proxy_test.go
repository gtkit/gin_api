package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
)

// newProxyProbeEngine 造一个只回显客户端 IP 的最小 engine，
// 走与生产同一个 applyTrustedProxies 与同一个 ginc.ClientIP。
func newProxyProbeEngine(opt RouterOptions) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	applyTrustedProxies(engine, opt)
	engine.GET("/whoami", func(c *gin.Context) {
		c.String(http.StatusOK, ginc.ClientIP(c))
	})
	return engine
}

func requestClientIP(t *testing.T, engine *gin.Engine, remoteAddr string, headers map[string]string) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("probe returned %d", rec.Code)
	}
	return rec.Body.String()
}

// TestClientIPIgnoresForgedHeadersByDefault 是本 change 的核心判据：
// 不配置可信代理时，客户端伪造的转发头必须被完全忽略。
//
// 未修复前 Gin 默认信任 0.0.0.0/0，这里会返回 203.0.113.9（伪造值）。
func TestClientIPIgnoresForgedHeadersByDefault(t *testing.T) {
	t.Parallel()

	engine := newProxyProbeEngine(RouterOptions{})

	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "no headers", headers: nil},
		{name: "forged x-forwarded-for", headers: map[string]string{"X-Forwarded-For": "203.0.113.9"}},
		{name: "forged x-real-ip", headers: map[string]string{"X-Real-IP": "203.0.113.9"}},
		{name: "both forged", headers: map[string]string{
			"X-Forwarded-For": "203.0.113.9", "X-Real-IP": "203.0.113.10",
		}},
		// 链式伪造：攻击者可以塞任意长的链条。
		{name: "forged chain", headers: map[string]string{"X-Forwarded-For": "203.0.113.9, 198.51.100.7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := requestClientIP(t, engine, "192.0.2.55:1234", tt.headers)
			if got != "192.0.2.55" {
				t.Fatalf("client IP = %q, want the real TCP peer 192.0.2.55 (forged headers must be ignored)", got)
			}
		})
	}
}

// TestClientIPTrustsConfiguredProxy 覆盖正常部署形态：
// 请求确实来自可信代理网段时，配置声明的转发头才被采信。
func TestClientIPTrustsConfiguredProxy(t *testing.T) {
	t.Parallel()

	engine := newProxyProbeEngine(RouterOptions{
		TrustedProxies:  []string{"10.0.0.0/8"},
		RemoteIPHeaders: []string{"X-Forwarded-For"},
	})

	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "trusted peer forwards real client",
			remoteAddr: "10.1.2.3:5678",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.7"},
			want:       "198.51.100.7",
		},
		{
			// 可信网段之外的直连请求，header 必须被忽略——否则任何人绕过代理直连即可伪造。
			name:       "untrusted peer is not believed",
			remoteAddr: "192.0.2.55:1234",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.7"},
			want:       "192.0.2.55",
		},
		{
			// 未在 remote_ip_headers 里列出的头不参与取值（Gin 默认会认 X-Real-IP）。
			name:       "unlisted header is not consulted",
			remoteAddr: "10.1.2.3:5678",
			headers:    map[string]string{"X-Real-IP": "198.51.100.7"},
			want:       "10.1.2.3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := requestClientIP(t, engine, tt.remoteAddr, tt.headers); got != tt.want {
				t.Fatalf("client IP = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestApplyTrustedProxiesOverridesGinDefaults 锁住两个默认值必须被显式覆盖：
// 不覆盖 RemoteIPHeaders 时 Gin 会保留 ["X-Forwarded-For","X-Real-IP"]，
// 于是"未在配置里列出的头"照样会被采信。
func TestApplyTrustedProxiesOverridesGinDefaults(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	if len(engine.RemoteIPHeaders) == 0 {
		t.Skip("gin no longer ships default RemoteIPHeaders; this guard is obsolete")
	}

	applyTrustedProxies(engine, RouterOptions{})
	if len(engine.RemoteIPHeaders) != 0 {
		t.Fatalf("RemoteIPHeaders = %v, want empty when not configured", engine.RemoteIPHeaders)
	}
}

// TestApplyTrustedProxiesFallsBackToTrustNothing 覆盖降级方向：
// 配置无法应用时唯一安全的方向是"谁都不信任"，绝不能留着 Gin 的默认值。
func TestApplyTrustedProxiesFallsBackToTrustNothing(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// 非法条目正常会被 config.Validate 拦下；这里直接绕过校验注入，验证装配层的降级方向。
	applyTrustedProxies(engine, RouterOptions{
		TrustedProxies:  []string{"definitely-not-a-cidr"},
		RemoteIPHeaders: []string{"X-Forwarded-For"},
	})

	engine.GET("/whoami", func(c *gin.Context) { c.String(http.StatusOK, ginc.ClientIP(c)) })
	got := requestClientIP(t, engine, "192.0.2.55:1234", map[string]string{"X-Forwarded-For": "203.0.113.9"})
	if got != "192.0.2.55" {
		t.Fatalf("client IP = %q, want the real peer: an unusable proxy config must degrade to trusting nothing", got)
	}
}
