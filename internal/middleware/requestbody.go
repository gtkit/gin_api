package middleware

import (
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// RequestBody 按路由契约治理请求体。
//
// 三件事，按代价从低到高排列，让最便宜的检查先拒绝：
//  1. **不接受请求体的路由带了 body** → 400。放行它等于让接口接受一份
//     谁也不会读的数据，而那正是绕过大小与类型约束的入口。
//  2. **Content-Type 不匹配** → 415。只接受 JSON 的接口收到 multipart 时，
//     不在这里拒绝的话，错误要到 handler 绑定失败才浮现，且此时 body 已被读完。
//  3. **字节上限** → 413。路由级上限只能比全局更严，由注册期保证。
//
// 位置在**认证之前**：一个超大 body 不该先耗掉认证的开销，内容类型不符
// 也不必等到 handler 绑定才发现。
//
// **这条顺序取决于声明位置，不取决于哪个中间件写在前面**：gin 的组中间件恒
// 先于路由级中间件执行，所以"认证挂在组上、本中间件挂在路由上"时，无论路由级
// 参数怎么排，认证都会先跑。要兑现该顺序，本中间件必须与认证声明在同一层或
// 更外层——按请求体形态划分子组是一种做法（见 internal/module/user 的 router）。
// JSONBody 要求请求体是 JSON 并限制其大小，用法与任何 gin 中间件一致：
//
//	g.POST("/thing", middleware.JSONBody(1<<20), h.Create)
//
// maxBytes <= 0 表示不额外收紧，沿用全局上限（见 MaxBytesLimit）。
func JSONBody(maxBytes int64) gin.HandlerFunc {
	return bodyGuard("application/json", maxBytes)
}

// FormBody 要求请求体是表单编码并限制其大小。
func FormBody(maxBytes int64) gin.HandlerFunc {
	return bodyGuard("application/x-www-form-urlencoded", maxBytes)
}

// MultipartBody 要求请求体是 multipart 表单（文件上传）并限制其大小。
func MultipartBody(maxBytes int64) gin.HandlerFunc {
	return bodyGuard("multipart/form-data", maxBytes)
}

// NoBody 拒绝携带请求体的请求。
//
// 显式拒绝而不是忽略：一个被静默忽略的 body 意味着客户端以为自己传了参数，
// 而服务端从未读过它——那种不一致只有在业务结果不对时才会被发现。
func NoBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		if hasBody(c.Request) {
			resp.BadRequestErr(c, "该接口不接受请求体")
			return
		}
		c.Next()
	}
}

// bodyGuard 是各 body 中间件的共同实现。
func bodyGuard(wantType string, maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		// maxBytes <= 0 时不在这里收紧：全局的 MaxBytesLimit 已经作为兜底
		// 挂在最外层，覆盖包括 404/405 在内的所有路径。这里只表达"这条路由
		// 比全局更严"。
		limit := maxBytes

		// 只在确实带 body 时校验类型：Content-Length 为 0 的写请求交给
		// handler 的绑定去报"字段缺失"，那个错误比 415 更贴近真实问题。
		if hasBody(c.Request) {
			if !contentTypeMatches(c.GetHeader("Content-Type"), wantType) {
				resp.UnsupportedMediaTypeErr(c, "该接口只接受 "+wantType)
				return
			}
			if limit > 0 && c.Request.ContentLength > limit {
				c.Header("Connection", "close")
				resp.PayloadTooLargeErr(c, "请求体过大，最大支持 "+resp.FormatBytes(limit), limit)
				return
			}
			// chunked 请求没有声明长度，由读取时的 MaxBytesReader 兜底。
			if limit > 0 {
				c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
			}
		}

		c.Next()
	}
}

// hasBody 报告请求是否携带（或声称携带）请求体。
//
// ContentLength 为 -1 表示 chunked——长度未知但确实有 body，不能当成没有。
func hasBody(r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	return r.ContentLength != 0
}

// contentTypeMatches 比较媒体类型，忽略参数与大小写。
//
// 必须解析而不是字符串前缀比较：真实请求的 Content-Type 常带参数
// （`application/json; charset=utf-8`、multipart 的 `boundary=...`），
// 直接相等比较会把完全合法的请求拒掉。
func contentTypeMatches(header, want string) bool {
	if header == "" {
		return false
	}
	parsed, _, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed, want)
}
