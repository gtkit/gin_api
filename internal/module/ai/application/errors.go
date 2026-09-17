package application

import "errors"

// 业务错误。transport 层按类型映射状态码，不解析文本。
//
// 上游厂商返回的错误由**装配层的 adapter 翻译成这里的哨兵错误**：application
// 不能 import LLM SDK（那会让业务层绑死一个具体的库），也不该按错误文本判断
// ——各厂商的文案不一致且随时会变。
var (
	// ErrConversationNotFound 表示会话不存在**或**不属于当前用户。
	//
	// 两种情况共用一个错误是刻意的：区分它们等于提供一条不需要任何权限
	// 就能枚举他人会话 ID 的信道。
	ErrConversationNotFound = errors.New("ai: conversation not found")

	// ErrEmptyMessage 表示本轮提问为空。
	//
	// 在任何上游调用之前就能判定，因此单独成错：合并进"上游拒绝了请求"
	// 会让一次明显的客户端错误先付掉一次真实的 API 调用。
	ErrEmptyMessage = errors.New("ai: message is empty")

	// ErrUnknownProvider 表示请求指定了一个未启用的厂商。
	//
	// 与"上游不可用"分开：前者是调用方写错了名字（400），后者是我们这边
	// 的依赖出了问题（503）。合并成一个会让客户端对着一个自己能修的错误重试。
	ErrUnknownProvider = errors.New("ai: provider is not enabled")

	// ErrUpstreamRateLimited 表示厂商侧限流。
	ErrUpstreamRateLimited = errors.New("ai: provider rate limited the request")

	// ErrContextTooLong 表示请求超出模型的上下文窗口。
	//
	// 它是**可由调用方自行修复**的：换个上下文更长的模型、或开一个新会话。
	// 因此不能映射成 5xx——那会让客户端一直重试同一个必然失败的请求。
	ErrContextTooLong = errors.New("ai: request exceeds the model context window")

	// ErrContentFiltered 表示内容被厂商的安全策略拦下。
	ErrContentFiltered = errors.New("ai: content was rejected by the provider safety filter")

	// ErrUpstreamAuth 表示厂商拒绝了我们的密钥。
	//
	// 这是**本服务的配置问题**，不是调用方的：密钥过期、欠费、被回收。
	// 因此它不能表达成 401/403——那等于把自己的运维故障说成客户端没有权限。
	ErrUpstreamAuth = errors.New("ai: provider rejected the configured credentials")

	// ErrUpstreamUnavailable 是其余一切上游故障：网络、5xx、熔断、超时。
	ErrUpstreamUnavailable = errors.New("ai: provider is unavailable")
)
