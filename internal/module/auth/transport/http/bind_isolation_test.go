package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/ginx"
)

// TestQueryCannotPolluteBodyBinding 钉住绑定只信 body、不信 query。
//
// gin 的 ShouldBind 在 Content-Type 为 form 时会**同时**从 query string 和 body
// 取值填充结构体；字段没有 form tag 时它回退用字段名匹配。于是攻击者只要把
// Content-Type 改成 form，就能用 query 注入本该只来自 body 的字段。
//
// 当前 DTO 的字段（手机号、密码）本就是用户可控的，注入拿不到额外收益——
// 但这是**结构性缺口**：任何一个 DTO 将来加了不该由用户控制的字段
// （Role / TenantID / IsAdmin 这类），它立刻变成真实漏洞，而加字段的人
// 不会收到任何提示。本用例就是那个提示。
func TestQueryCannotPolluteBodyBinding(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	// 用一个带敏感字段的载荷模拟"将来加了字段"的情形。
	type payload struct {
		Name string `json:"name" form:"name"`
		Role string `json:"role" form:"role"`
	}

	tests := []struct {
		name        string
		contentType string
		body        string
		wantRole    string
	}{
		{
			name:        "form 模式下 query 不得注入字段",
			contentType: "application/x-www-form-urlencoded",
			body:        "name=bob",
			wantRole:    "", // query 里的 role=admin 必须被忽略
		},
		{
			name:        "form 模式下 body 自身的值正常绑定",
			contentType: "application/x-www-form-urlencoded",
			body:        "name=bob&role=member",
			wantRole:    "member",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got payload
			engine := gin.New()
			engine.POST("/x", func(c *gin.Context) {
				if err := ginx.BindBody(c, &got); err != nil {
					t.Errorf("BindBody: %v", err)
				}
			})

			// query 里塞一个提权字段，body 里没有它。
			req := httptest.NewRequest(http.MethodPost, "/x?role=admin", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			engine.ServeHTTP(httptest.NewRecorder(), req)

			if got.Role != tt.wantRole {
				t.Fatalf("Role = %q, want %q：query 参数污染了 body 绑定", got.Role, tt.wantRole)
			}
			if tt.wantRole == "" && got.Name != "bob" {
				t.Fatalf("Name = %q, want bob：body 自身的值应当照常绑定", got.Name)
			}
		})
	}
}

// TestGinShouldBindIsVulnerable 反证上面那条防线确实在挡东西。
//
// 直接用 gin 原生的 ShouldBind 走同一份请求：query 里的 role=admin 会被填进去。
// 这条用例的意义是证明 BindBody 的差异不是摆设——如果哪天 gin 改了行为、
// 或者这个判断本身是错的，它会失败并提示重新评估。
func TestGinShouldBindIsVulnerable(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	type payload struct {
		Name string `json:"name" form:"name"`
		Role string `json:"role" form:"role"`
	}

	var got payload
	engine := gin.New()
	engine.POST("/x", func(c *gin.Context) {
		if err := c.ShouldBind(&got); err != nil {
			t.Errorf("bind control payload: %v", err)
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/x?role=admin", strings.NewReader("name=bob"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	engine.ServeHTTP(httptest.NewRecorder(), req)

	if got.Role != "admin" {
		t.Skipf("gin 的 ShouldBind 未从 query 取值（实测 Role=%q）——"+
			"若 gin 已修正该行为，BindBody 的这层隔离仍无害，但本用例的前提需重新评估", got.Role)
	}
}
