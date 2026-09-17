package http

import (
	"github.com/gtkit/goerr"

	"github.com/gtkit/gin-api/internal/module/ai/application"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// errMapper 把 ai 领域错误映射为对客状态码。
//
// 选 Status 时看的是它的 **HTTP 码**，不是名字。goerr 里有一批"业务码 +
// HTTP 200"型的状态（如 StatusQuotaExceeded、StatusBusinessRuleViolation），
// 名字读起来正合适，实际会让一次失败返回 200——客户端按状态码写的分支全部失效。
//
// 分档的依据是**谁能修好它**：
//
//	调用方能修  → 4xx（改参数、换模型、开新会话）
//	我们要修    → 5xx（密钥过期、上游宕机、熔断打开）
//
// 把后者说成 4xx 最有害：客户端会去改一个自己改不了的东西，同时我们的告警
// 也不会响——4xx 通常不计入服务健康度。
var errMapper = resp.NewErrMapper(
	resp.ErrMapping{
		// 会话不存在与"不是你的会话"共用一条映射：application 已经把两者合并，
		// 这里若再拆开，防会话枚举的努力会在最后一步失效。
		Target: application.ErrConversationNotFound,
		Status: goerr.StatusRecordNotFound(), Message: "会话不存在",
	},
	resp.ErrMapping{
		Target: application.ErrEmptyMessage,
		Status: goerr.StatusValidateParams(), Message: "内容不能为空",
	},
	resp.ErrMapping{
		// 厂商名写错是调用方的参数问题，不是"上游不可用"。合并成 503
		// 会让客户端对着一个自己能修的错误反复重试。
		Target: application.ErrUnknownProvider,
		Status: goerr.StatusValidateParams(), Message: "该模型厂商未启用",
	},
	resp.ErrMapping{
		// 429 而不是 503：客户端与网关对 429 有既定处置（退避重试），
		// 而 503 常被当作"整个服务挂了"从而触发不必要的故障转移。
		Target: application.ErrUpstreamRateLimited,
		Status: goerr.StatusTooManyRequests(), Message: "请求过于频繁，请稍后再试",
	},
	resp.ErrMapping{
		// 上下文超长是**可由调用方自行修复**的：换个上下文更长的模型，
		// 或开一个新会话。映射成 5xx 会让客户端一直重试同一个必然失败的请求。
		Target: application.ErrContextTooLong,
		Status: goerr.StatusValidateParams(), Message: "对话内容过长，请开启新会话或更换模型",
	},
	resp.ErrMapping{
		Target: application.ErrContentFiltered,
		Status: goerr.StatusValidateParams(), Message: "内容未通过安全审核",
	},
	resp.ErrMapping{
		// 密钥被上游拒绝是**我们的**配置问题，不是调用方没有权限。
		// 用 401/403 表达等于把自己的运维故障说成客户端的鉴权失败——
		// 客户端会去重新登录，而那永远修不好它。
		Target: application.ErrUpstreamAuth,
		Status: goerr.StatusDependencyUnavailable(), Message: "AI 服务暂不可用",
	},
	resp.ErrMapping{
		Target: application.ErrUpstreamUnavailable,
		Status: goerr.StatusDependencyUnavailable(), Message: "AI 服务暂不可用",
	},
)
