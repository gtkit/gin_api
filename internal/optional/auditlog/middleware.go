package auditlog

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
)

// Middleware 在请求完成后投递一条写操作审计记录。
//
// 挂在需要审计的路由组上（通常是写操作组，认证之后——身份要从
// 已认证上下文取）。enqueue 为 nil 时返回直接放行的中间件，
// 与 Metrics 的 nil recorder 同款契约：「没接审计」是正常路径。
//
// 记录在 c.Next() 之后构造：panic 请求不产生审计记录——panic 由更外层
// GinRecovery 兜底并已进错误日志与访问日志；在 recovery 写 500 之前
// 记录到的状态码是未定稿的，宁缺毋错。
//
// 刻意不记请求体、响应体与 query string（脱敏事故的既往来源，
// 访问日志已按各自受控策略覆盖）。字符串字段按列宽在此截断：
// 严格 sql_mode 下 varchar 溢出是整批写入失败，一条超长记录
// 会拖死同批的全部记录。
func Middleware(enqueue func(Record) bool) gin.HandlerFunc {
	if enqueue == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			// 未匹配路由（404/405）没有模板，回退原始路径以保留审计线索。
			route = c.Request.URL.Path
		}
		// 丢弃与否由记录器计数并节流告警，这里不重复处理。
		_ = enqueue(Record{
			OccurredAt: start,
			Method:     truncateRunes(c.Request.Method, methodLen),
			Route:      truncateRunes(route, routeLen),
			Status:     c.Writer.Status(),
			LatencyMs:  time.Since(start).Milliseconds(),
			UserID:     ginc.UserIDFromContext(c),
			RequestID:  truncateRunes(middleware.RequestIDFromContext(c), requestIDLen),
			ClientIP:   truncateRunes(ginc.ClientIP(c), clientIPLen),
		})
	}
}

// truncateRunes 按字符数截断（MySQL varchar(n) 计的是字符不是字节）。
func truncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s // 字节数不超限则字符数必不超限，跳过解码。
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
