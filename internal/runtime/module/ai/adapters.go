package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	llm "github.com/gtkit/go-llm-provider/v2/provider"

	aiapp "github.com/gtkit/gin-api/internal/module/ai/application"
)

// entry 是一家已启用厂商的装配产物。
type entry struct {
	provider llm.Provider
	// model 是**生效**的默认模型：配置里填了就是它，留空则取库内预设。
	//
	// 单独存一份是因为 llm.Provider 接口只有 Name/Chat/ChatStream，
	// 拿不到模型名；而落库与 /providers 都要报告真实取值。
	model string
}

// llmClient 把 go-llm-provider 适配成 application 的 LLMClient port。
//
// **不使用库自带的 llm.Registry**：它按名字存 Provider，但存不下"这家生效的
// 默认模型是什么"。用它就得再维护一个平行的 model map，于是"有哪些厂商"
// 出现两份可能不一致的事实。一个 map 装两样东西没有这个问题。
type llmClient struct {
	providers map[string]entry
	// defaultName 是配置里的默认厂商；装配期已校验它在 providers 里。
	defaultName string
	// names 是有序厂商名，避免每次 Providers() 都重新排序。
	names []string
}

// Providers 返回已启用的厂商列表（按名字有序）。
func (c *llmClient) Providers() []aiapp.ProviderInfo {
	infos := make([]aiapp.ProviderInfo, 0, len(c.names))
	for _, name := range c.names {
		infos = append(infos, aiapp.ProviderInfo{
			Name:    name,
			Model:   c.providers[name].model,
			Default: name == c.defaultName,
		})
	}
	return infos
}

// resolve 按请求指定的厂商名取用，留空时取默认。
func (c *llmClient) resolve(name string) (entry, error) {
	if name == "" {
		name = c.defaultName
	}
	e, ok := c.providers[name]
	if !ok {
		// 带上可选集合：调用方多半只是把名字写错了，而一句"不支持"
		// 不足以让它自己改对。
		return entry{}, fmt.Errorf("%w: %q (enabled: %v)", aiapp.ErrUnknownProvider, name, c.names)
	}
	return e, nil
}

// Chat 发起一次非流式调用。
func (c *llmClient) Chat(ctx context.Context, call aiapp.ChatCall) (aiapp.Reply, error) {
	e, err := c.resolve(call.Provider)
	if err != nil {
		return aiapp.Reply{}, err
	}

	resp, err := e.provider.Chat(ctx, buildRequest(call))
	if err != nil {
		return aiapp.Reply{}, translateError(err)
	}

	return aiapp.Reply{
		Content:      resp.Content,
		Reasoning:    resp.Reasoning,
		Provider:     string(e.provider.Name()),
		Model:        effectiveModel(call.Model, e.model),
		FinishReason: resp.FinishReason,
		Usage:        translateUsage(resp.Usage),
	}, nil
}

// Stream 发起一次流式调用。
//
// 返回的 Reply 在出错时仍携带**已收到的增量**：用户已经在屏幕上看到那些字，
// 上层据此把部分结果落库（见 LLMClient port 的契约）。
func (c *llmClient) Stream(
	ctx context.Context, call aiapp.ChatCall, onDelta func(string) error,
) (aiapp.Reply, error) {
	e, err := c.resolve(call.Provider)
	if err != nil {
		return aiapp.Reply{}, err
	}

	stream, err := e.provider.ChatStream(ctx, buildRequest(call))
	if err != nil {
		return aiapp.Reply{}, translateError(err)
	}
	// Close 的错误只记不抛：流已经读完（或已经出错），关闭失败改变不了结果，
	// 用它覆盖掉真正的错误只会让排查走错方向。
	defer func() { _ = stream.Close() }()

	reply := aiapp.Reply{
		Provider: string(e.provider.Name()),
		Model:    effectiveModel(call.Model, e.model),
	}
	var content, reasoning strings.Builder

	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			reply.Content = content.String()
			reply.Reasoning = reasoning.String()
			return reply, translateError(recvErr)
		}
		if chunk == nil {
			continue
		}

		// 模型名以响应侧回传的为准：请求留空时它才是真实取值。
		// 只有部分 chunk 携带非空值，取最后一个非空的。
		if chunk.Model != "" {
			reply.Model = chunk.Model
		}
		if chunk.FinishReason != "" {
			reply.FinishReason = chunk.FinishReason
		}
		// 用量随流尾部的 chunk 到达，且可能出现在 FinishReason 之后、
		// io.EOF 之前——采用最后一个非零值（见 StreamChunk.Usage 的说明）。
		if chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			reply.Usage = translateUsage(chunk.Usage)
		}

		if chunk.ReasoningDelta != "" {
			reasoning.WriteString(chunk.ReasoningDelta)
		}
		if chunk.Delta == "" {
			continue
		}
		content.WriteString(chunk.Delta)
		if cbErr := onDelta(chunk.Delta); cbErr != nil {
			// 写回客户端失败之后继续读上游没有意义，只是白付剩余 token 的钱。
			reply.Content = content.String()
			reply.Reasoning = reasoning.String()
			return reply, cbErr
		}
	}

	reply.Content = content.String()
	reply.Reasoning = reasoning.String()
	return reply, nil
}

// buildRequest 把中立的 ChatCall 翻译成 SDK 请求。
func buildRequest(call aiapp.ChatCall) *llm.ChatRequest {
	messages := make([]llm.Message, 0, len(call.Messages))
	for _, m := range call.Messages {
		messages = append(messages, llm.Message{
			Role:    llm.Role(m.Role),
			Content: []llm.ContentPart{llm.TextPart(m.Content)},
		})
	}

	return &llm.ChatRequest{
		// 留空即用 provider 构造时的默认模型；显式传入才覆盖。
		Model:       call.Model,
		Messages:    messages,
		MaxTokens:   call.MaxTokens,
		Temperature: call.Temperature,
	}
}

func translateUsage(u llm.Usage) aiapp.Usage {
	return aiapp.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// effectiveModel 返回本次调用实际生效的模型名。
func effectiveModel(requested, fallback string) string {
	if requested != "" {
		return requested
	}
	return fallback
}

// translateError 把 SDK 错误翻译成 application 的领域错误。
//
// 这一层是**边界的价值所在**：application 因此既不 import SDK，也不按错误
// 文本判断——各厂商的文案不一致且随时会变，而按文本匹配的分支会在某次
// 上游文案调整后静默失效。
//
// 分类依据是 ProviderError.Code，它由库按 HTTP 状态与厂商错误码归一化。
// 拿不到结构化错误时（网络层的裸 error、ctx 取消）落到"上游不可用"——
// 那是唯一不会误导调用方的兜底：它意味着"重试可能有用"，而把未知错误说成
// "你的请求有问题" 会让调用方去改一个没问题的请求。
func translateError(err error) error {
	if err == nil {
		return nil
	}
	// ctx 取消/超时原样上抛：上层要用 errors.Is(err, context.Canceled) 区分
	// "客户端主动断开"与真正的故障，包成领域错误会让那个判断失效。
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	perr, ok := errors.AsType[*llm.ProviderError](err)
	if !ok {
		return fmt.Errorf("%w: %w", aiapp.ErrUpstreamUnavailable, err)
	}

	switch perr.Code {
	case llm.ErrorCodeRateLimit:
		return fmt.Errorf("%w: %w", aiapp.ErrUpstreamRateLimited, err)
	case llm.ErrorCodeContextLength:
		return fmt.Errorf("%w: %w", aiapp.ErrContextTooLong, err)
	case llm.ErrorCodeContentFilter:
		return fmt.Errorf("%w: %w", aiapp.ErrContentFiltered, err)
	case llm.ErrorCodeAuth:
		return fmt.Errorf("%w: %w", aiapp.ErrUpstreamAuth, err)
	case llm.ErrorCodeInvalidRequest:
		// 参数非法是**本服务**拼错了请求（我们控制着全部字段），不是调用方的错。
		// 映射成"上游不可用"会让它被当成可重试的故障——重试同一个非法请求
		// 永远不会成功，但至少不会让调用方去改一个自己改不了的东西。
		// 真正的修法是看日志里的原始错误。
		return fmt.Errorf("%w: %w", aiapp.ErrUpstreamUnavailable, err)
	case llm.ErrorCodeTimeout, llm.ErrorCodeServerError, llm.ErrorCodeNetwork, llm.ErrorCodeUnknown:
		return fmt.Errorf("%w: %w", aiapp.ErrUpstreamUnavailable, err)
	default:
		return fmt.Errorf("%w: %w", aiapp.ErrUpstreamUnavailable, err)
	}
}

// logAuditor 把 application 的审计 port 适配到进程级 logger。
type logAuditor struct {
	logf func(format string, args ...any)
}

func (a logAuditor) Auditf(format string, args ...any) {
	if a.logf == nil {
		return
	}
	a.logf(format, args...)
}

// sortedNames 返回排序后的厂商名副本。
func sortedNames(providers map[string]entry) []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
