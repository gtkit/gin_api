// Package http 是 AI 对话模块的 HTTP 适配层。
package http

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/ginx"
	"github.com/gtkit/ssex"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/module/ai/application"
	"github.com/gtkit/gin-api/internal/module/ai/dto"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// Timeouts 是本模块两条对话路由的时长参数，由装配层传入**已补齐默认值**的生效值。
//
// 由装配层交出来而不是让本层再读一次配置：两处各自补默认值必然分叉，
// 而分叉的表现是"配置写的 60s，实际按 30s 掐断"，没有任何地方能看出来。
type Timeouts struct {
	// Request 是非流式对话的业务预算。
	Request time.Duration
	// StreamTotal 是流式对话从起流到收尾的整体预算。
	StreamTotal time.Duration
	// Heartbeat 是 SSE 保活帧间隔。
	Heartbeat time.Duration
}

// 零值兜底。
//
// 只服务于单测手工构造 Deps 的场景——生产装配恒传生效值。不兜底的话，
// middleware.Timeout 对非正数直接 panic，而那会把"测试忘了填一个字段"
// 表现成一次装配期崩溃。
const (
	fallbackRequestTimeout  = 60 * time.Second
	fallbackStreamTotal     = 5 * time.Minute
	fallbackHeartbeatPeriod = 15 * time.Second
)

// resolved 返回补齐零值后的时长。
func (t Timeouts) resolved() Timeouts {
	if t.Request <= 0 {
		t.Request = fallbackRequestTimeout
	}
	if t.StreamTotal <= 0 {
		t.StreamTotal = fallbackStreamTotal
	}
	if t.Heartbeat <= 0 {
		t.Heartbeat = fallbackHeartbeatPeriod
	}
	return t
}

// Handler 是 AI 对话端点。
type Handler struct {
	service  *application.Service
	timeouts Timeouts
}

// NewHandler 构造 Handler。
func NewHandler(service *application.Service, timeouts Timeouts) *Handler {
	return &Handler{service: service, timeouts: timeouts.resolved()}
}

// Providers 返回已启用的厂商与其默认模型。
func (h *Handler) Providers(c *gin.Context) {
	infos := h.service.Providers()
	items := make([]dto.ProviderResponse, 0, len(infos))
	for _, info := range infos {
		items = append(items, dto.ProviderResponse{
			Name: info.Name, Model: info.Model, Default: info.Default,
		})
	}
	resp.Ok(c, items)
}

// Chat 执行一次非流式对话。
func (h *Handler) Chat(c *gin.Context) {
	var req dto.ChatRequest
	if err := ginx.BindBody(c, &req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	result, err := h.service.Chat(c.Request.Context(), application.ChatCommand{
		UserID:         ginc.UserIDFromContext(c),
		ConversationID: req.ConversationID,
		Message:        req.Message,
		Provider:       req.Provider,
		Model:          req.Model,
	})
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}

	resp.Ok(c, dto.ChatResponse{
		ConversationID: result.ConversationID,
		Title:          result.Title,
		MessageID:      result.MessageID,
		Content:        result.Content,
		Reasoning:      result.Reasoning,
		Provider:       result.Provider,
		Model:          result.Model,
		FinishReason:   result.FinishReason,
		Usage:          usageResponse(result.Usage),
	})
}

// ChatStream 以 SSE 推送增量回复。
//
// 事件契约：
//
//	delta  每个增量文本片段
//	done   已落库的收尾（partial 区分正常结束与中途打断）
//	error  失败且没有任何内容落库（终态，之后不会再有 done）
//
// **起流时机是懒的**：第一个 delta 到达（或心跳到点）之前不写任何字节。
// 这样"提问为空""会话不存在""密钥被上游拒绝"这些在首字节之前就能判定的
// 失败，仍然能走正常 JSON 信封并带上正确的状态码——响应头一旦发出就再也
// 改不了状态码了。
//
// 心跳同时承担两件事：给长时间不吐字的流保活（模型思考期间代理会按空闲断连），
// 以及在那种情况下**代替第一个 delta 起流**，从而解除连接级写截止时间。
func (h *Handler) ChatStream(c *gin.Context) {
	var req dto.ChatRequest
	if err := ginx.BindBody(c, &req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	// 流式的整体预算在这里给出，而不是靠路由级 middleware.Timeout——
	// 那个中间件的协作式 deadline 会把正常生成中的长流一并掐断。
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.timeouts.StreamTotal)
	defer cancel()

	stream := ssex.NewStream(c.Writer, c.Request)

	// 心跳跑在**独立的 ctx** 上：handler 因终态、写失败或应用停机而返回时
	// （此时请求 ctx 可能还没取消）也要能停掉它。
	//
	// 错误信号与退出信号必须是两个 channel：用同一个兼任时，主流程消费掉
	// 唯一那次发送后，下面的 <-hbDone 就永远等不到——handler 永久阻塞。
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		if err := stream.Heartbeat(hbCtx, h.timeouts.Heartbeat); err != nil &&
			!errors.Is(err, context.Canceled) {
			logger.Debugf("ai: sse heartbeat stopped: %v", err)
		}
	}()

	result, err := h.service.StreamChat(ctx, application.ChatCommand{
		UserID:         ginc.UserIDFromContext(c),
		ConversationID: req.ConversationID,
		Message:        req.Message,
		Provider:       req.Provider,
		Model:          req.Model,
	}, func(delta string) error {
		return stream.Event("delta", dto.StreamDelta{Content: delta})
	})

	// **必须等心跳 goroutine 真的退出**，再决定怎么收尾。
	//
	// 两个理由，缺一不可：
	//  1. 它在 handler 之外的 goroutine 里往 c.Writer 写，而 gin 在 handler
	//     返回后会把 ResponseWriter 归还对象池——晚一步写出去的帧会落到
	//     另一个请求的响应上。
	//  2. 下面要读 stream.Started() 来决定走 JSON 还是走事件，而心跳自己
	//     也会起流。不等它退出，这个判断就是个竞态。
	stopHeartbeat()
	<-hbDone

	h.finishStream(c, stream, result, err)
}

// finishStream 按"流是否已经开始"选择收尾形态。
func (h *Handler) finishStream(
	c *gin.Context, stream *ssex.Stream, result application.ChatResult, err error,
) {
	if err != nil && result.ConversationID == 0 {
		// 什么都没落库。流还没开始的话，仍然可以回一个带正确状态码的 JSON。
		if !stream.Started() {
			resp.MapErr(c, err, errMapper)
			return
		}
		// 已经开始了：状态码早已发出，只能经事件体表达。
		// 对客文案走与 JSON 分支同一套映射，避免两条路径说法不一。
		emit(stream, "error", dto.StreamError{Message: streamErrorMessage(err)})
		return
	}

	if err != nil {
		// 已保存部分结果：走 done 而不是 error——客户端的处置与正常结束相同
		//（拿 conversation_id 继续聊），差别只是要不要提示"回答不完整"。
		logger.Warnf("ai: stream interrupted after %d chars were persisted: %v",
			len([]rune(result.Content)), err)
	}

	emit(stream, "done", dto.StreamDone{
		ConversationID: result.ConversationID,
		Title:          result.Title,
		MessageID:      result.MessageID,
		Provider:       result.Provider,
		Model:          result.Model,
		FinishReason:   result.FinishReason,
		Partial:        result.Partial,
		Usage:          usageResponse(result.Usage),
	})
}

// emit 写出一条 SSE 事件，失败只记日志。
//
// 到这一步已经没有别的出口了：响应头早已发出，客户端多半就是断开了。
// 把它上抛给 gin 只会让错误中间件试图再写一次响应体。
func emit(stream *ssex.Stream, name string, payload any) {
	if err := stream.Event(name, payload); err != nil {
		logger.Debugf("ai: write sse %q event: %v", name, err)
	}
}

// streamErrorMessage 取与 JSON 分支同源的对客文案。
//
// 不直接回 err.Error()：那是给运维看的内部错误，可能含上游返回的原始信息
// （含端点、模型名、有时还有请求片段）。未命中映射时给一句中性文案，
// 具体原因留在日志里。
func streamErrorMessage(err error) string {
	if item := errMapper.Resolve(err); item != nil {
		return item.Message()
	}
	logger.Errorf("ai: unmapped stream failure: %v", err)
	return "AI 服务暂不可用"
}

// ListConversations 分页列出当前用户的会话。
func (h *Handler) ListConversations(c *gin.Context) {
	var req dto.ConversationListRequest
	// 这里用 ShouldBindQuery 是对的：列表参数的来源本就是 query。
	if err := c.ShouldBindQuery(&req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	items, page, err := h.service.ListConversations(
		c.Request.Context(), ginc.UserIDFromContext(c), req.Query())
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}

	list := make([]dto.ConversationResponse, 0, len(items))
	for _, conv := range items {
		list = append(list, conversationResponse(conv))
	}
	resp.Ok(c, dto.ConversationListResponse{Items: list, Paging: page})
}

// GetConversation 返回会话详情与一页消息。
func (h *Handler) GetConversation(c *gin.Context) {
	id, ok := conversationID(c)
	if !ok {
		return
	}

	var req dto.MessageListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	detail, err := h.service.Conversation(
		c.Request.Context(), ginc.UserIDFromContext(c), id, req.Query())
	if err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}

	messages := make([]dto.MessageResponse, 0, len(detail.Messages))
	for _, msg := range detail.Messages {
		messages = append(messages, messageResponse(msg))
	}
	resp.Ok(c, dto.ConversationDetailResponse{
		Conversation: conversationResponse(detail.Conversation),
		Messages:     messages,
		Paging:       detail.Paging,
	})
}

// RenameConversation 改写会话标题。
func (h *Handler) RenameConversation(c *gin.Context) {
	id, ok := conversationID(c)
	if !ok {
		return
	}

	var req dto.RenameRequest
	if err := ginx.BindBody(c, &req); err != nil {
		resp.BindStructErr(c, err)
		return
	}

	if err := h.service.RenameConversation(
		c.Request.Context(), ginc.UserIDFromContext(c), id, req.Title); err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, nil)
}

// DeleteConversation 软删会话。
func (h *Handler) DeleteConversation(c *gin.Context) {
	id, ok := conversationID(c)
	if !ok {
		return
	}

	if err := h.service.DeleteConversation(
		c.Request.Context(), ginc.UserIDFromContext(c), id); err != nil {
		resp.MapErr(c, err, errMapper)
		return
	}
	resp.Ok(c, nil)
}

// conversationID 解析路径参数；非法时已写出 422 响应，返回 ok=false。
//
// 不用 ShouldBindUri + 结构体：一个 int64 路径参数值不上一个结构体，
// 而"解析失败即 422"的处置在三个 handler 里完全一致。
func conversationID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		resp.BadRequestErr(c, "会话 ID 不合法")
		return 0, false
	}
	return id, true
}

func usageResponse(u application.Usage) dto.UsageResponse {
	return dto.UsageResponse{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

func conversationResponse(conv *models.AIConversation) dto.ConversationResponse {
	if conv == nil {
		return dto.ConversationResponse{}
	}
	out := dto.ConversationResponse{
		ID:               conv.ID,
		Title:            conv.Title,
		Provider:         conv.Provider,
		Model:            conv.Model,
		MessageCount:     conv.MessageCount,
		PromptTokens:     conv.PromptTokens,
		CompletionTokens: conv.CompletionTokens,
		CreatedAt:        conv.CreatedAt,
		UpdatedAt:        conv.UpdatedAt,
	}
	if conv.LastMessageAt != nil {
		out.LastMessageAt = *conv.LastMessageAt
	}
	return out
}

func messageResponse(msg *models.AIMessage) dto.MessageResponse {
	if msg == nil {
		return dto.MessageResponse{}
	}
	return dto.MessageResponse{
		ID:               msg.ID,
		Role:             msg.Role.String(),
		Content:          msg.Content,
		Reasoning:        msg.Reasoning,
		Provider:         msg.Provider,
		Model:            msg.Model,
		PromptTokens:     msg.PromptTokens,
		CompletionTokens: msg.CompletionTokens,
		TotalTokens:      msg.TotalTokens,
		FinishReason:     msg.FinishReason,
		CreatedAt:        msg.CreatedAt,
	}
}
