package ginc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{name: "plain peer", remoteAddr: "192.0.2.55:1234", want: "192.0.2.55"},
		{
			// engine 未配置可信代理时（本测试用裸 gin.New() + SetTrustedProxies(nil)），
			// 伪造头必须被忽略。
			name:       "forged header ignored when no proxy trusted",
			remoteAddr: "192.0.2.55:1234",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.9"},
			want:       "192.0.2.55",
		},
		{name: "ipv6 peer", remoteAddr: "[2001:db8::1]:1234", want: "2001:db8::1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gin.SetMode(gin.TestMode)
			engine := gin.New()
			if err := engine.SetTrustedProxies(nil); err != nil {
				t.Fatalf("SetTrustedProxies: %v", err)
			}
			engine.RemoteIPHeaders = nil

			var got string
			engine.GET("/x", func(c *gin.Context) { got = ClientIP(c) })

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			engine.ServeHTTP(httptest.NewRecorder(), req)

			if got != tt.want {
				t.Fatalf("ClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClientIPNilContext 守住 accessor 的健壮性：它是全仓唯一入口，
// 不该因为某个调用方传了 nil 就 panic 掉整个请求。
func TestClientIPNilContext(t *testing.T) {
	t.Parallel()

	if got := ClientIP(nil); got != "" {
		t.Fatalf("ClientIP(nil) = %q, want empty string", got)
	}
}
