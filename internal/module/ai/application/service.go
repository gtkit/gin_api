package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/pkg/paging"
)

// Service 编排 AI 对话：组装上下文 → 调模型 → 落库。
type Service struct {
	store ConversationStore
	llm   LLMClient

	auditor Auditor

	systemPrompt    string
	historyMessages int
	maxTokens       int
	temperature     *float32

	// savePartialBudget 是"客户端断开后把已生成内容落库"的独立预算。
	//
	// 必须独立：那一刻请求 ctx 已经取消，继续用它做任何数据库写入都会立刻失败，
	// 于是用户已经看到的内容在历史里凭空消失。
	savePartialBudget time.Duration

	// now 可注入，测试用它确定性地驱动时间相关断言。
	now func() time.Time
}

// Deps 是 Service 的必需依赖。缺失即构造失败——依赖不全的 service 不允许进入运行期。
type Deps struct {
	Store ConversationStore
	LLM   LLMClient
}

// Option 配置可选行为。
type Option func(*Service)

// WithAuditor 注入审计日志出口。
func WithAuditor(a Auditor) Option {
	return func(s *Service) { s.auditor = a }
}

// WithSystemPrompt 设置每轮拼在最前面的系统提示词。
func WithSystemPrompt(prompt string) Option {
	return func(s *Service) { s.systemPrompt = prompt }
}

// WithHistoryMessages 设置组装上下文时回溯的最大历史消息条数。
//
// 0 是**有效取值**（不带历史，每轮独立问答），因此这里不做正数判断——
// 判正会让"关掉历史"这个配置静默变成"用默认值"。
func WithHistoryMessages(n int) Option {
	return func(s *Service) {
		if n >= 0 {
			s.historyMessages = n
		}
	}
}

// WithMaxTokens 设置单次回复的输出上限；<=0 表示不下发该参数。
func WithMaxTokens(n int) Option {
	return func(s *Service) { s.maxTokens = n }
}

// WithTemperature 设置采样温度；nil 表示不下发该参数。
func WithTemperature(t *float32) Option {
	return func(s *Service) { s.temperature = t }
}

// WithClock 注入时钟，供测试使用。
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// defaultSavePartialBudget 是断流落库的兜底预算。
//
// 取 5 秒：它只做一次本地事务写入，正常在毫秒级完成；给足余量是因为这条路径
// 恰恰发生在系统可能已经不健康的时候（上游超时、客户端大量断开）。
const defaultSavePartialBudget = 5 * time.Second

// NewService 构造服务；依赖不全即返回 error。
//
// 返回 error 而不是 panic：装配缺失是启动期错误，应当以错误表达并一路上抛到
// 启动流程，在进程启动时报出并退出，而不是留到第一个请求。
func NewService(deps Deps, opts ...Option) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("ai: conversation store is required")
	}
	if deps.LLM == nil {
		return nil, errors.New("ai: llm client is required")
	}

	svc := &Service{
		store:             deps.Store,
		llm:               deps.LLM,
		historyMessages:   -1, // 哨兵：未经 option 设置时取下方默认
		savePartialBudget: defaultSavePartialBudget,
	}
	for _, opt := range opts {
		opt(svc)
	}
	if svc.historyMessages < 0 {
		svc.historyMessages = 20
	}

	return svc, nil
}

func (s *Service) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) auditf(format string, args ...any) {
	if s.auditor == nil {
		return
	}
	s.auditor.Auditf(format, args...)
}

// ChatCommand 是一次对话请求。
type ChatCommand struct {
	UserID int64
	// ConversationID 为 0 表示开启新会话。
	ConversationID int64
	Message        string
	// Provider / Model 为空表示走配置默认。
	Provider string
	Model    string
}

// ChatResult 是一次对话的结果。
type ChatResult struct {
	ConversationID int64
	Title          string
	MessageID      int64
	Content        string
	Reasoning      string
	Provider       string
	Model          string
	FinishReason   string
	Usage          Usage
	// Partial 表示这是一次**未正常结束**的生成（客户端断开、上游中断），
	// 已生成的部分仍然落了库。
	Partial bool
}

// Providers 返回已启用的厂商列表。
func (s *Service) Providers() []ProviderInfo {
	return s.llm.Providers()
}

// Chat 执行一次非流式对话。
func (s *Service) Chat(ctx context.Context, cmd ChatCommand) (ChatResult, error) {
	prepared, err := s.prepare(ctx, cmd)
	if err != nil {
		return ChatResult{}, err
	}

	reply, err := s.llm.Chat(ctx, prepared.call)
	if err != nil {
		return ChatResult{}, err
	}

	return s.persist(ctx, prepared, reply, false)
}

// StreamChat 执行一次流式对话，每个增量片段调用一次 onDelta。
//
// **中途失败时已生成的部分仍然落库**：用户已经在屏幕上看到了那些字，历史里
// 却什么都没有，是这个功能最容易被投诉也最难解释的一种表现。落库用独立预算的
// context.WithoutCancel——那一刻请求 ctx 通常已经取消，用它写库必然失败。
//
// 返回值在 err != nil 时仍可能是有效结果（Partial 为真、ConversationID 已回填），
// 调用方据此把会话标识告诉客户端。
func (s *Service) StreamChat(
	ctx context.Context, cmd ChatCommand, onDelta func(delta string) error,
) (ChatResult, error) {
	if onDelta == nil {
		return ChatResult{}, errors.New("ai: stream requires a delta callback")
	}

	prepared, err := s.prepare(ctx, cmd)
	if err != nil {
		return ChatResult{}, err
	}

	reply, streamErr := s.llm.Stream(ctx, prepared.call, onDelta)
	if streamErr == nil {
		return s.persist(ctx, prepared, reply, false)
	}

	// 一个字都没生成：没有值得保留的东西，直接把错误交回去。
	// 这里落一条空 assistant 记录反而有害——它会作为空正文进入下一轮的历史
	//（组装时被过滤掉），并把 message_count 推得与实际内容对不上。
	if strings.TrimSpace(reply.Content) == "" {
		return ChatResult{}, streamErr
	}

	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.savePartialBudget)
	defer cancel()

	result, saveErr := s.persist(saveCtx, prepared, reply, true)
	if saveErr != nil {
		// 落库也失败了：把两个错误一起上抛，但**先报流的错误**——它才是根因，
		// 落库失败多半只是它的后果（ctx 已取消、库正好也不健康）。
		s.auditf("ai: partial reply of user %d was lost: %v", cmd.UserID, saveErr)
		return ChatResult{}, errors.Join(streamErr, saveErr)
	}

	s.auditf("ai: saved partial reply of user %d in conversation %d (%d chars)",
		cmd.UserID, result.ConversationID, len([]rune(result.Content)))
	return result, streamErr
}

// preparedTurn 是 prepare 的产物：本轮调用参数与它所属的会话。
type preparedTurn struct {
	// conv 是目标会话；新会话时 ID 为 0，由 SaveTurn 在事务内创建。
	conv     *models.AIConversation
	question string
	call     ChatCall
	// isNew 表示这是会话的第一轮，用于决定是否生成标题。
	isNew bool
}

// prepare 校验入参、解析会话、组装上下文。
//
// 它刻意**不写任何库**：会话直到模型成功回复才创建（见 SaveTurn 的说明）。
func (s *Service) prepare(ctx context.Context, cmd ChatCommand) (preparedTurn, error) {
	if cmd.UserID <= 0 {
		return preparedTurn{}, errors.New("ai: user id is required")
	}

	question, err := normalizeMessage(cmd.Message)
	if err != nil {
		return preparedTurn{}, err
	}

	conv := &models.AIConversation{UserID: cmd.UserID}
	isNew := true
	var history []*models.AIMessage

	if cmd.ConversationID > 0 {
		existing, found, getErr := s.store.GetOwned(ctx, cmd.UserID, cmd.ConversationID)
		if getErr != nil {
			return preparedTurn{}, fmt.Errorf("load conversation: %w", getErr)
		}
		if !found {
			return preparedTurn{}, ErrConversationNotFound
		}
		conv = existing
		isNew = false

		if s.historyMessages > 0 {
			history, err = s.store.RecentMessages(ctx, conv.ID, s.historyMessages)
			if err != nil {
				return preparedTurn{}, fmt.Errorf("load conversation history: %w", err)
			}
		}
	}

	if isNew {
		conv.Title = buildTitle(question)
	}

	return preparedTurn{
		conv:     conv,
		question: question,
		isNew:    isNew,
		call: ChatCall{
			Provider:    strings.TrimSpace(cmd.Provider),
			Model:       strings.TrimSpace(cmd.Model),
			Messages:    assembleMessages(s.systemPrompt, history, question),
			MaxTokens:   s.maxTokens,
			Temperature: s.temperature,
		},
	}, nil
}

// finishReasonAborted 标记一次被中断的生成。
//
// 必须与正常结束区分：一个被截断的回答与一个完整的回答在正文上看不出区别，
// 而用户报"回答不完整"时，这是唯一能分辨的依据。
const finishReasonAborted = "aborted"

// persist 把一轮对话写进聚合仓储并组装返回结果。
func (s *Service) persist(
	ctx context.Context, prepared preparedTurn, reply Reply, partial bool,
) (ChatResult, error) {
	at := s.currentTime()

	finish := truncateRunes(reply.FinishReason, models.AIMessageFinishReasonLen)
	if partial {
		finish = finishReasonAborted
	}

	provider := truncateRunes(reply.Provider, models.AIMessageProviderLen)
	model := truncateRunes(reply.Model, models.AIMessageModelLen)

	user := &models.AIMessage{
		UserID:  prepared.conv.UserID,
		Role:    models.AIRoleUser,
		Content: prepared.question,
	}
	assistant := &models.AIMessage{
		UserID:           prepared.conv.UserID,
		Role:             models.AIRoleAssistant,
		Content:          reply.Content,
		Reasoning:        reply.Reasoning,
		Provider:         provider,
		Model:            model,
		PromptTokens:     reply.Usage.PromptTokens,
		CompletionTokens: reply.Usage.CompletionTokens,
		TotalTokens:      reply.Usage.TotalTokens,
		FinishReason:     finish,
	}

	prepared.conv.LastMessageAt = &at
	prepared.conv.Provider = truncateRunes(provider, models.AIConversationProviderLen)
	prepared.conv.Model = truncateRunes(model, models.AIConversationModelLen)

	if err := s.store.SaveTurn(ctx, prepared.conv, user, assistant); err != nil {
		return ChatResult{}, fmt.Errorf("save conversation turn: %w", err)
	}

	return ChatResult{
		ConversationID: prepared.conv.ID,
		Title:          prepared.conv.Title,
		MessageID:      assistant.ID,
		Content:        reply.Content,
		Reasoning:      reply.Reasoning,
		Provider:       reply.Provider,
		Model:          reply.Model,
		FinishReason:   finish,
		Usage:          reply.Usage,
		Partial:        partial,
	}, nil
}

// ListConversations 分页列出当前用户的会话。
func (s *Service) ListConversations(
	ctx context.Context, userID int64, query paging.Query,
) ([]*models.AIConversation, paging.Paging, error) {
	if userID <= 0 {
		return nil, paging.Paging{}, errors.New("ai: user id is required")
	}
	return s.store.List(ctx, userID, query)
}

// ConversationDetail 是会话详情：会话头 + 一页消息。
type ConversationDetail struct {
	Conversation *models.AIConversation
	Messages     []*models.AIMessage
	Paging       paging.Paging
}

// Conversation 取会话详情。
//
// 归属校验发生在这里的 GetOwned，随后的 ListMessages 不再重复校验——
// 这是本模块**唯一**一处按 conversationID 列消息的编排点，由它的测试盯住。
func (s *Service) Conversation(
	ctx context.Context, userID, id int64, query paging.Query,
) (ConversationDetail, error) {
	conv, found, err := s.store.GetOwned(ctx, userID, id)
	if err != nil {
		return ConversationDetail{}, fmt.Errorf("load conversation: %w", err)
	}
	if !found {
		return ConversationDetail{}, ErrConversationNotFound
	}

	messages, page, err := s.store.ListMessages(ctx, conv.ID, query)
	if err != nil {
		return ConversationDetail{}, fmt.Errorf("list conversation messages: %w", err)
	}

	return ConversationDetail{Conversation: conv, Messages: messages, Paging: page}, nil
}

// RenameConversation 改写会话标题。
func (s *Service) RenameConversation(ctx context.Context, userID, id int64, title string) error {
	normalized := strings.Join(strings.Fields(title), " ")
	if normalized == "" {
		return ErrEmptyMessage
	}
	normalized = truncateRunes(normalized, models.AIConversationTitleLen)

	updated, err := s.store.Rename(ctx, userID, id, normalized)
	if err != nil {
		return fmt.Errorf("rename conversation: %w", err)
	}
	if !updated {
		return ErrConversationNotFound
	}
	return nil
}

// DeleteConversation 软删会话。
func (s *Service) DeleteConversation(ctx context.Context, userID, id int64) error {
	deleted, err := s.store.Delete(ctx, userID, id)
	if err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	if !deleted {
		return ErrConversationNotFound
	}
	s.auditf("ai: user %d deleted conversation %d", userID, id)
	return nil
}
