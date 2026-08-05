package middleware

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
)

// Metrics 记录每个请求的量、时延与状态码。
//
// recorder 为 nil 时返回一个直接放行的中间件：那让"没接指标"成为一条正常
// 路径，而不是要求装配处写判空。
func Metrics(recorder *metrics.Recorder) gin.HandlerFunc {
	if recorder == nil {
		return func(c *gin.Context) { c.Next() }
	}

	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		// **路由标签取 FullPath()，不是 c.Request.URL.Path**。
		//
		// FullPath 返回注册时的路由模板（如 `/api/v1/user/:id`），
		// 因此同一路由的所有请求共用一条时间序列。用实际路径的话，
		// 每个不同的 id 都会产出一条新序列——序列数随业务数据量增长，
		// 而监控后端会先于业务被压垮。
		route := c.FullPath()
		if route == "" {
			// 没有匹配到任何路由（404，或方法不允许时的 405）。
			//
			// 这条分支比上面那条更要紧：未匹配路径完全由请求方控制，
			// 一次目录爆破就能凭空造出成千上万条序列。全部归到一个常量上，
			// 扫描流量因此只体现为一条序列上的计数增长——那既是安全的，
			// 也正好是运维想看的形态（"未匹配请求突然变多"）。
			route = metrics.UnmatchedRoute
		}

		recorder.RecordRequest(
			c.Request.Context(),
			c.Request.Method,
			route,
			c.Writer.Status(),
			time.Since(start),
		)
	}
}
