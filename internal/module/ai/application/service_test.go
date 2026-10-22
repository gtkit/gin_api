package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/pkg/paging"
)

// fakeStore 是 ConversationStore 的手写替身。
//
// 手写而不是用 mock 框架：本层要断言的是**编排顺序与传下去的内容**
// （有没有先校归属、上下文里放了什么、落库的两条消息长什么样），
// 而这些用几个记录字段就能表达，生成的 mock 反而把它们藏进匹配器里。
type fakeStore struct {
	mu sync.Mutex

	conv      *models.AIConversation
	convFound bool
	getErr    error
	getCalls  int

	history    []*models.AIMessage
	historyErr error
	// historyLimit 记录最后一次 RecentMessages 的 limit，用于断言窗口生效。
	historyLimit int
	historyCalls int

	savedConv      *models.AIConversation
	savedUser      *models.AIMessage
	savedAssistant *models.AIMessage
	saveErr        error
	// saveCtxErr 记录 SaveTurn 收到的 ctx 当时的 Err()，
	// 用来证明断流落库没有沿用那个已经取消的 ctx。
	saveCtxErr error
	saveCalls  int

	messages    []*models.AIMessage
	listMsgCall int

	renamed   bool
	renameErr error
	deleted   bool
	deleteErr error
}

func (f *fakeStore) SaveTurn(
	ctx context.Context, conv *models.AIConversation, user, assistant *models.AIMessage,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.saveCalls++
	f.saveCtxErr = ctx.Err()
	if f.saveErr != nil {
		return f.saveErr
	}
	if conv.ID == 0 {
		conv.ID = 42
	}
	user.ID, user.ConversationID = 100, conv.ID
	assistant.ID, assistant.ConversationID = 101, conv.ID
	f.savedConv, f.savedUser, f.savedAssistant = conv, user, assistant
	return nil
}

func (f *fakeStore) GetOwned(
	_ context.Context, _, _ int64,
) (*models.AIConversation, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.getCalls++
	return f.conv, f.convFound, f.getErr
}

func (f *fakeStore) List(
	context.Context, int64, paging.Query,
) ([]*models.AIConversation, paging.Paging, error) {
	return []*models.AIConversation{f.conv}, paging.Paging{CurrentPage: 1}, nil
}

func (f *fakeStore) RecentMessages(
	_ context.Context, _ int64, limit int,
) ([]*models.AIMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.historyCalls++
	f.historyLimit = limit
	return f.history, f.historyErr
}

func (f *fakeStore) ListMessages(
	context.Context, int64, paging.Query,
) ([]*models.AIMessage, paging.Paging, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.listMsgCall++
	return f.messages, paging.Paging{CurrentPage: 1}, nil
}

func (f *fakeStore) Rename(context.Context, int64, int64, string) (bool, error) {
	return f.renamed, f.renameErr
}

func (f *fakeStore) Delete(context.Context, int64, int64) (bool, error) {
	return f.deleted, f.deleteErr
}

// fakeLLM 是 LLMClient 的手写替身。
type fakeLLM struct {
	// call 记录最后一次收到的调用参数。
	call ChatCall
	// calls 计数，用于断言"参数非法时根本没调上游"。
	calls int

	reply Reply
	err   error

	// deltas 是流式要吐出的片段。
	deltas []string
}

func (f *fakeLLM) Providers() []ProviderInfo {
	return []ProviderInfo{{Name: "deepseek", Model: "deepseek-chat", Default: true}}
}

func (f *fakeLLM) Chat(_ context.Context, call ChatCall) (Reply, error) {
	f.calls++
	f.call = call
	return f.reply, f.err
}

func (f *fakeLLM) Stream(_ context.Context, call ChatCall, onDelta func(string) error) (Reply, error) {
	f.calls++
	f.call = call

	var sb strings.Builder
	for _, d := range f.deltas {
		if cbErr := onDelta(d); cbErr != nil {
			reply := f.reply
			reply.Content = sb.String()
			return reply, cbErr
		}
		if _, err := sb.WriteString(d); err != nil {
			reply := f.reply
			reply.Content = sb.String()
			return reply, err
		}
	}

	reply := f.reply
	if f.err != nil {
		// 出错时仍带回已生成的部分——这是 port 契约里最要紧的一条。
		reply.Content = sb.String()
		return reply, f.err
	}
	if reply.Content == "" {
		reply.Content = sb.String()
	}
	return reply, nil
}

func newService(t *testing.T, store *fakeStore, llm *fakeLLM, opts ...Option) *Service {
	t.Helper()

	svc, err := NewService(Deps{Store: store, LLM: llm}, opts...)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestNewServiceRejectsMissingDependencies 钉住依赖不全即构造失败。
//
// 依赖不全的 service 不允许进入运行期：留到第一个请求才空指针，
// 那条错误既指不回装配点，也已经影响到真实用户。
func TestNewServiceRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	if _, err := NewService(Deps{LLM: &fakeLLM{}}); err == nil {
		t.Error("a service without a store was constructed")
	}
	if _, err := NewService(Deps{Store: &fakeStore{}}); err == nil {
		t.Error("a service without an llm client was constructed")
	}
}

// TestChatStartsANewConversationWithAGeneratedTitle 钉住首轮的完整产出。
func TestChatStartsANewConversationWithAGeneratedTitle(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	llm := &fakeLLM{reply: Reply{
		Content:      "Go 是一门静态类型语言。",
		Provider:     "deepseek",
		Model:        "deepseek-chat",
		FinishReason: "stop",
		Usage:        Usage{PromptTokens: 12, CompletionTokens: 20, TotalTokens: 32},
	}}
	svc := newService(t, store, llm)

	result, err := svc.Chat(context.Background(), ChatCommand{UserID: 7, Message: " 介绍 Go "})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if result.ConversationID == 0 {
		t.Error("the result carries no conversation id: the client cannot continue the chat")
	}
	if result.Title != "介绍 Go" {
		t.Errorf("Title = %q, want the trimmed question", result.Title)
	}
	if store.savedUser.Content != "介绍 Go" || store.savedUser.Role != models.AIRoleUser {
		t.Errorf("the user message was not persisted as asked: %+v", store.savedUser)
	}
	if store.savedAssistant.Content != llm.reply.Content ||
		store.savedAssistant.Role != models.AIRoleAssistant {
		t.Errorf("the assistant message was not persisted as returned: %+v", store.savedAssistant)
	}
	if store.savedAssistant.TotalTokens != 32 {
		t.Errorf("usage was not persisted: %+v", store.savedAssistant)
	}
	if store.savedConv.LastMessageAt == nil {
		t.Error("last_message_at was not set: the conversation would sort to the bottom of the list")
	}
	// 首轮不查历史：没有历史可查，一次多余的往返而已。
	if store.historyCalls != 0 {
		t.Errorf("history was loaded for a brand new conversation (%d calls)", store.historyCalls)
	}
}

// TestChatReplaysHistoryOfAnExistingConversation 钉住续聊会带上历史。
func TestChatReplaysHistoryOfAnExistingConversation(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		conv:      &models.AIConversation{ID: 5, UserID: 7, Title: "旧标题"},
		convFound: true,
		history: []*models.AIMessage{
			{Role: models.AIRoleUser, Content: "第一问"},
			{Role: models.AIRoleAssistant, Content: "第一答"},
		},
	}
	llm := &fakeLLM{reply: Reply{Content: "第二答", Provider: "qwen", Model: "qwen-max"}}
	svc := newService(t, store, llm, WithHistoryMessages(4), WithSystemPrompt("你是助手"))

	result, err := svc.Chat(context.Background(), ChatCommand{
		UserID: 7, ConversationID: 5, Message: "第二问",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if store.historyLimit != 4 {
		t.Errorf("RecentMessages limit = %d, want the configured 4", store.historyLimit)
	}
	if got := len(llm.call.Messages); got != 4 {
		t.Fatalf("the model received %d messages, want system+2 history+question: %+v",
			got, llm.call.Messages)
	}
	if llm.call.Messages[0].Role != RoleSystem {
		t.Errorf("the system prompt is not first: %+v", llm.call.Messages)
	}
	// 续聊不重写标题：用户可能已经改过它。
	if result.Title != "旧标题" {
		t.Errorf("Title = %q: an existing conversation must keep its title", result.Title)
	}
}

// TestChatWithoutHistoryWindowSkipsTheQuery 钉住 history_messages=0 时不查库。
//
// 0 是**有效取值**（每轮独立问答）。把它当成"没配"退回默认，等于配置里
// 关掉的东西实际还开着，而多出的那次查询与多付的输入 token 都无人知晓。
func TestChatWithoutHistoryWindowSkipsTheQuery(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		conv:      &models.AIConversation{ID: 5, UserID: 7},
		convFound: true,
		history:   []*models.AIMessage{{Role: models.AIRoleUser, Content: "不该出现"}},
	}
	llm := &fakeLLM{reply: Reply{Content: "答"}}
	svc := newService(t, store, llm, WithHistoryMessages(0))

	if _, err := svc.Chat(context.Background(), ChatCommand{
		UserID: 7, ConversationID: 5, Message: "问",
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if store.historyCalls != 0 {
		t.Errorf("history was loaded although the window is 0 (%d calls)", store.historyCalls)
	}
	if len(llm.call.Messages) != 1 {
		t.Errorf("history leaked into the context: %+v", llm.call.Messages)
	}
}

// TestChatRefusesAConversationThatIsNotYours 钉住越权访问在调上游**之前**被拒。
//
// 顺序要紧：先调模型再校归属的话，一次越权尝试仍然会产生真实账单，
// 而攻击者只要不断换 ID 就能持续消耗预算。
func TestChatRefusesAConversationThatIsNotYours(t *testing.T) {
	t.Parallel()

	store := &fakeStore{convFound: false}
	llm := &fakeLLM{}
	svc := newService(t, store, llm)

	_, err := svc.Chat(context.Background(), ChatCommand{UserID: 7, ConversationID: 5, Message: "问"})
	if !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("Chat = %v, want ErrConversationNotFound", err)
	}
	if llm.calls != 0 {
		t.Error("the provider was called for a conversation the caller does not own: " +
			"每次越权尝试都会产生真实账单")
	}
	if store.saveCalls != 0 {
		t.Error("something was persisted for a conversation the caller does not own")
	}
}

// TestChatRejectsAnEmptyQuestionBeforeSpending 钉住空提问不触发上游调用。
func TestChatRejectsAnEmptyQuestionBeforeSpending(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	llm := &fakeLLM{}
	svc := newService(t, store, llm)

	_, err := svc.Chat(context.Background(), ChatCommand{UserID: 7, Message: "   "})
	if !errors.Is(err, ErrEmptyMessage) {
		t.Fatalf("Chat = %v, want ErrEmptyMessage", err)
	}
	if llm.calls != 0 || store.saveCalls != 0 {
		t.Error("an empty question reached the provider or the database")
	}
}

// TestChatDoesNotPersistWhenTheProviderFails 钉住上游失败时不留空会话。
//
// 先建会话再调模型的话，一次上游失败会在列表里留下一条什么都没有的记录——
// 既不能续聊，也说不清它是什么。
func TestChatDoesNotPersistWhenTheProviderFails(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	llm := &fakeLLM{err: ErrUpstreamUnavailable}
	svc := newService(t, store, llm)

	if _, err := svc.Chat(context.Background(), ChatCommand{UserID: 7, Message: "问"}); err == nil {
		t.Fatal("Chat succeeded although the provider failed")
	}
	if store.saveCalls != 0 {
		t.Error("an empty conversation was left behind after an upstream failure")
	}
}

// TestStreamChatForwardsEveryDeltaAndPersistsOnce 钉住流式的正常路径。
func TestStreamChatForwardsEveryDeltaAndPersistsOnce(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	llm := &fakeLLM{
		deltas: []string{"Go ", "是一门", "静态类型语言"},
		reply: Reply{
			Provider: "deepseek", Model: "deepseek-chat", FinishReason: "stop",
			Usage: Usage{TotalTokens: 30},
		},
	}
	svc := newService(t, store, llm)

	var seen []string
	result, err := svc.StreamChat(context.Background(),
		ChatCommand{UserID: 7, Message: "介绍 Go"},
		func(delta string) error { seen = append(seen, delta); return nil })
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	if strings.Join(seen, "") != "Go 是一门静态类型语言" {
		t.Errorf("deltas forwarded to the client = %q", strings.Join(seen, ""))
	}
	if result.Partial {
		t.Error("a stream that completed normally was reported as partial")
	}
	if store.saveCalls != 1 {
		t.Errorf("SaveTurn was called %d times, want exactly 1", store.saveCalls)
	}
	if store.savedAssistant.FinishReason != "stop" {
		t.Errorf("finish reason = %q, want the provider's own", store.savedAssistant.FinishReason)
	}
}

// TestStreamChatPersistsWhatTheUserAlreadySaw 钉住断流时已生成的部分仍然落库。
//
// 这是这个功能最容易被投诉也最难解释的一种表现：用户在屏幕上看到了那些字，
// 历史里却什么都没有。落库还必须**换一个没被取消的 ctx**——那一刻请求 ctx
// 通常已经取消，用它写库必然失败。
func TestStreamChatPersistsWhatTheUserAlreadySaw(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	llm := &fakeLLM{
		deltas: []string{"已经", "生成的部分"},
		err:    ErrUpstreamUnavailable,
	}
	svc := newService(t, store, llm)

	ctx, cancel := context.WithCancel(context.Background())
	// 模拟客户端断开：请求 ctx 在流失败之前就已经取消。
	cancel()

	result, err := svc.StreamChat(ctx, ChatCommand{UserID: 7, Message: "问"},
		func(string) error { return nil })

	if !errors.Is(err, ErrUpstreamUnavailable) {
		t.Fatalf("StreamChat error = %v, want the upstream failure to surface", err)
	}
	if store.saveCalls != 1 {
		t.Fatalf("SaveTurn was called %d times: what the user already read would be lost",
			store.saveCalls)
	}
	if store.saveCtxErr != nil {
		t.Errorf("SaveTurn ran on the canceled request context (ctx.Err()=%v): "+
			"every write would fail exactly when it matters", store.saveCtxErr)
	}
	if !result.Partial {
		t.Error("the result is not marked partial: the client cannot tell the answer was cut off")
	}
	if store.savedAssistant.FinishReason != finishReasonAborted {
		t.Errorf("finish reason = %q, want %q: a truncated answer is indistinguishable "+
			"from a complete one in the body alone",
			store.savedAssistant.FinishReason, finishReasonAborted)
	}
	if store.savedAssistant.Content != "已经生成的部分" {
		t.Errorf("persisted content = %q, want everything the client received",
			store.savedAssistant.Content)
	}
	if result.ConversationID == 0 {
		t.Error("no conversation id came back: the client cannot resume this conversation")
	}
}

// TestStreamChatSavesNothingWhenNoTokenWasGenerated 钉住"一个字都没生成"时不写空记录。
//
// 空 assistant 记录有害：它会作为空正文进入下一轮历史（组装时被过滤），
// 并把 message_count 推得与实际内容对不上。
func TestStreamChatSavesNothingWhenNoTokenWasGenerated(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	llm := &fakeLLM{err: ErrUpstreamAuth}
	svc := newService(t, store, llm)

	_, err := svc.StreamChat(context.Background(), ChatCommand{UserID: 7, Message: "问"},
		func(string) error { return nil })
	if !errors.Is(err, ErrUpstreamAuth) {
		t.Fatalf("StreamChat = %v, want ErrUpstreamAuth", err)
	}
	if store.saveCalls != 0 {
		t.Error("an empty assistant message was persisted")
	}
}

// TestStreamChatRequiresACallback 钉住 nil 回调是编码错误。
func TestStreamChatRequiresACallback(t *testing.T) {
	t.Parallel()

	svc := newService(t, &fakeStore{}, &fakeLLM{})
	if _, err := svc.StreamChat(context.Background(),
		ChatCommand{UserID: 7, Message: "问"}, nil); err == nil {
		t.Error("StreamChat accepted a nil delta callback")
	}
}

// TestConversationChecksOwnershipBeforeListingMessages 钉住详情接口的归属校验。
//
// ListMessages 刻意不自己校归属（每次列消息多一次往返），因此这条编排是
// **唯一**的保障——它失效就意味着任何人凭猜 ID 就能读到别人的全部对话。
func TestConversationChecksOwnershipBeforeListingMessages(t *testing.T) {
	t.Parallel()

	store := &fakeStore{convFound: false}
	svc := newService(t, store, &fakeLLM{})

	_, err := svc.Conversation(context.Background(), 7, 5, paging.Query{})
	if !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("Conversation = %v, want ErrConversationNotFound", err)
	}
	if store.listMsgCall != 0 {
		t.Error("messages were listed for a conversation the caller does not own")
	}
}

// TestConversationReturnsHeaderAndMessages 钉住详情的正常路径。
func TestConversationReturnsHeaderAndMessages(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		conv:      &models.AIConversation{ID: 5, UserID: 7, Title: "标题"},
		convFound: true,
		messages:  []*models.AIMessage{{Role: models.AIRoleUser, Content: "问"}},
	}
	svc := newService(t, store, &fakeLLM{})

	detail, err := svc.Conversation(context.Background(), 7, 5, paging.Query{})
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}
	if detail.Conversation == nil || detail.Conversation.ID != 5 || len(detail.Messages) != 1 {
		t.Errorf("unexpected detail: %+v", detail)
	}
}

// TestRenameAndDeleteReportMissingConversations 钉住"没改到/没删到"变成 not found。
//
// 返回 nil 的话，一次针对别人会话的改名请求会得到成功响应——调用方据此
// 以为改动生效，而实际什么都没发生。
func TestRenameAndDeleteReportMissingConversations(t *testing.T) {
	t.Parallel()

	svc := newService(t, &fakeStore{renamed: false, deleted: false}, &fakeLLM{})

	if err := svc.RenameConversation(context.Background(), 7, 5, "新标题"); !errors.Is(
		err, ErrConversationNotFound) {
		t.Errorf("RenameConversation = %v, want ErrConversationNotFound", err)
	}
	if err := svc.DeleteConversation(context.Background(), 7, 5); !errors.Is(
		err, ErrConversationNotFound) {
		t.Errorf("DeleteConversation = %v, want ErrConversationNotFound", err)
	}

	ok := newService(t, &fakeStore{renamed: true, deleted: true}, &fakeLLM{})
	if err := ok.RenameConversation(context.Background(), 7, 5, "新标题"); err != nil {
		t.Errorf("RenameConversation: %v", err)
	}
	if err := ok.DeleteConversation(context.Background(), 7, 5); err != nil {
		t.Errorf("DeleteConversation: %v", err)
	}
}

// TestRenameRejectsAWhitespaceOnlyTitle 钉住空标题被拒。
func TestRenameRejectsAWhitespaceOnlyTitle(t *testing.T) {
	t.Parallel()

	svc := newService(t, &fakeStore{renamed: true}, &fakeLLM{})
	if err := svc.RenameConversation(context.Background(), 7, 5, "  \n "); !errors.Is(
		err, ErrEmptyMessage) {
		t.Errorf("RenameConversation = %v, want ErrEmptyMessage", err)
	}
}

// TestSamplingOptionsReachTheProvider 钉住配置里的采样参数确实下发。
func TestSamplingOptionsReachTheProvider(t *testing.T) {
	t.Parallel()

	temp := float32(0)
	llm := &fakeLLM{reply: Reply{Content: "答"}}
	svc := newService(t, &fakeStore{}, llm,
		WithMaxTokens(256), WithTemperature(&temp),
		WithClock(func() time.Time { return time.Unix(1700000000, 0) }))

	if _, err := svc.Chat(context.Background(), ChatCommand{UserID: 7, Message: "问"}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if llm.call.MaxTokens != 256 {
		t.Errorf("MaxTokens = %d, want 256", llm.call.MaxTokens)
	}
	// temperature=0 必须真的下发：它与"没配"是两件事，而零值判定分不开。
	if llm.call.Temperature == nil || *llm.call.Temperature != 0 {
		t.Errorf("Temperature = %v, want an explicit 0", llm.call.Temperature)
	}
}

// TestProvidersIsForwarded 钉住厂商列表来自客户端而不是本层臆造。
func TestProvidersIsForwarded(t *testing.T) {
	t.Parallel()

	svc := newService(t, &fakeStore{}, &fakeLLM{})
	got := svc.Providers()
	if len(got) != 1 || got[0].Name != "deepseek" || !got[0].Default {
		t.Errorf("Providers() = %+v", got)
	}
}
