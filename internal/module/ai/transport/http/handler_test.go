package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/module/ai/application"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	"github.com/gtkit/gin-api/internal/pkg/paging"
)

// stubStore 是 application.ConversationStore 的最小替身。
//
// 本文件测的是 HTTP 适配（绑定、状态码、SSE 事件形态），不是编排逻辑——
// 后者在 application 包有覆盖。因此这里只需要能让 service 走通即可。
type stubStore struct {
	conv      *models.AIConversation
	convFound bool
	saveErr   error
	renamed   bool
	deleted   bool
}

func (s *stubStore) SaveTurn(
	_ context.Context, conv *models.AIConversation, user, assistant *models.AIMessage,
) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	if conv.ID == 0 {
		conv.ID = 9
	}
	user.ID, assistant.ID = 10, 11
	return nil
}

func (s *stubStore) GetOwned(context.Context, int64, int64) (*models.AIConversation, bool, error) {
	return s.conv, s.convFound, nil
}

func (s *stubStore) List(
	context.Context, int64, paging.Query,
) ([]*models.AIConversation, paging.Paging, error) {
	return []*models.AIConversation{s.conv}, paging.Paging{CurrentPage: 1, TotalCount: 1}, nil
}

func (s *stubStore) RecentMessages(context.Context, int64, int) ([]*models.AIMessage, error) {
	return nil, nil
}

func (s *stubStore) ListMessages(
	context.Context, int64, paging.Query,
) ([]*models.AIMessage, paging.Paging, error) {
	return []*models.AIMessage{
		{ID: 1, Role: models.AIRoleUser, Content: "问"},
	}, paging.Paging{CurrentPage: 1, TotalCount: 1}, nil
}

func (s *stubStore) Rename(context.Context, int64, int64, string) (bool, error) {
	return s.renamed, nil
}

func (s *stubStore) Delete(context.Context, int64, int64) (bool, error) {
	return s.deleted, nil
}

// stubLLM 是 application.LLMClient 的最小替身。
type stubLLM struct {
	reply  application.Reply
	err    error
	deltas []string
	// delay 是每个片段之间的停顿，用来构造"生成比预算慢"的场景。
	delay time.Duration
}

func (s *stubLLM) Providers() []application.ProviderInfo {
	return []application.ProviderInfo{{Name: "deepseek", Model: "deepseek-chat", Default: true}}
}

func (s *stubLLM) Chat(ctx context.Context, _ application.ChatCall) (application.Reply, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			// 真实 provider 也是这样：ctx 到点即放弃，不把结果硬塞回去。
			return application.Reply{}, ctx.Err()
		}
	}
	return s.reply, s.err
}

func (s *stubLLM) Stream(
	ctx context.Context, _ application.ChatCall, onDelta func(string) error,
) (application.Reply, error) {
	var sb strings.Builder
	for _, d := range s.deltas {
		if s.delay > 0 {
			select {
			case <-time.After(s.delay):
			case <-ctx.Done():
				reply := s.reply
				reply.Content = sb.String()
				return reply, ctx.Err()
			}
		}
		if err := onDelta(d); err != nil {
			reply := s.reply
			reply.Content = sb.String()
			return reply, err
		}
		sb.WriteString(d)
	}
	reply := s.reply
	reply.Content = sb.String()
	return reply, s.err
}

// newEngine 组装一个只带本模块路由的 engine，auth 中间件写入固定用户。
func newEngine(t *testing.T, store *stubStore, llm *stubLLM) *gin.Engine {
	t.Helper()
	return newEngineWith(t, store, llm, Timeouts{})
}

// newEngineWith 同 newEngine，但允许指定时长预算。
func newEngineWith(t *testing.T, store *stubStore, llm *stubLLM, timeouts Timeouts) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	svc, err := application.NewService(application.Deps{Store: store, LLM: llm})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	engine := gin.New()
	// 终态中间件必须在场：协作式超时不自己写响应，而是把 ctx 错误记进 gin 的
	// 错误链交由它裁决（超时 504、客户端断开不回包）。没有它，一次超时会表现为
	// 一个空的 200——而那正是本模块最容易被误判的一种失败。
	engine.Use(middleware.ErrorHandler())
	Register(engine.Group("/api/v1"), Deps{
		Service:  svc,
		Timeouts: timeouts,
		Auth: func(c *gin.Context) {
			ginc.SetUserID(c, 7)
			c.Next()
		},
	})
	return engine
}

func postJSON(t *testing.T, engine *gin.Engine, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// envelope 是统一响应信封中测试关心的部分。
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()

	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return env
}

// TestChatReturnsTheReplyAndUsage 钉住非流式对话的响应形态。
func TestChatReturnsTheReplyAndUsage(t *testing.T) {
	t.Parallel()

	engine := newEngine(t, &stubStore{}, &stubLLM{reply: application.Reply{
		Content: "回答", Provider: "deepseek", Model: "deepseek-chat",
		FinishReason: "stop", Usage: application.Usage{TotalTokens: 30},
	}})

	rec := postJSON(t, engine, "/api/v1/ai/chat", `{"message":"问题"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var data struct {
		ConversationID int64  `json:"conversation_id"`
		Content        string `json:"content"`
		FinishReason   string `json:"finish_reason"`
		Usage          struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(decode(t, rec).Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data.ConversationID == 0 || data.Content != "回答" || data.FinishReason != "stop" {
		t.Errorf("unexpected payload: %+v", data)
	}
	if data.Usage.TotalTokens != 30 {
		t.Error("usage was dropped: callers cannot attribute cost without it")
	}
}

// TestChatMapsDomainErrorsToHonestStatusCodes 钉住错误分档。
//
// 分档的依据是**谁能修好它**。把"我们的密钥过期"说成 4xx 最有害：
// 客户端会去改一个自己改不了的东西，而我们的告警也不会响。
func TestChatMapsDomainErrorsToHonestStatusCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{"会话不存在", application.ErrConversationNotFound, http.StatusNotFound},
		{"厂商未启用", application.ErrUnknownProvider, http.StatusUnprocessableEntity},
		{"上游限流", application.ErrUpstreamRateLimited, http.StatusTooManyRequests},
		{"上下文超长", application.ErrContextTooLong, http.StatusUnprocessableEntity},
		{"内容被拦", application.ErrContentFiltered, http.StatusUnprocessableEntity},
		{"密钥被拒", application.ErrUpstreamAuth, http.StatusServiceUnavailable},
		{"上游不可用", application.ErrUpstreamUnavailable, http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			engine := newEngine(t, &stubStore{}, &stubLLM{err: tt.err})
			rec := postJSON(t, engine, "/api/v1/ai/chat", `{"message":"问题"}`)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

// TestChatDoesNotLeakUpstreamErrorText 钉住对客文案不是原始错误。
//
// 原始错误常含端点、模型名，有时还有请求片段——那是给运维看的，不该出现在
// 客户端响应里。
func TestChatDoesNotLeakUpstreamErrorText(t *testing.T) {
	t.Parallel()

	secret := "https://internal-gateway.example/v1/chat/completions key=sk-REDACTED"
	engine := newEngine(t, &stubStore{},
		&stubLLM{err: errors.Join(application.ErrUpstreamUnavailable, errors.New(secret))})

	rec := postJSON(t, engine, "/api/v1/ai/chat", `{"message":"问题"}`)
	if strings.Contains(rec.Body.String(), "sk-REDACTED") ||
		strings.Contains(rec.Body.String(), "internal-gateway") {
		t.Errorf("the upstream error text reached the client: %s", rec.Body.String())
	}
}

// TestChatRejectsAnEmptyBody 钉住绑定层拦下缺字段的请求。
func TestChatRejectsAnEmptyBody(t *testing.T) {
	t.Parallel()

	engine := newEngine(t, &stubStore{}, &stubLLM{})
	rec := postJSON(t, engine, "/api/v1/ai/chat", `{}`)
	if rec.Code == http.StatusOK {
		t.Errorf("a request without a message was accepted: %s", rec.Body.String())
	}
}

// TestChatStreamEmitsDeltasThenDone 钉住 SSE 的正常事件序列。
func TestChatStreamEmitsDeltasThenDone(t *testing.T) {
	t.Parallel()

	engine := newEngine(t, &stubStore{}, &stubLLM{
		deltas: []string{"你", "好"},
		reply: application.Reply{
			Provider: "deepseek", Model: "deepseek-chat", FinishReason: "stop",
		},
	})

	rec := postJSON(t, engine, "/api/v1/ai/chat/stream", `{"message":"问题"}`)

	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream (body %s)", got, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Count(body, "event: delta") != 2 {
		t.Errorf("expected two delta events, got:\n%s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("the stream did not end with a done event:\n%s", body)
	}
	if strings.Contains(body, `"partial":true`) {
		t.Errorf("a stream that completed normally was marked partial:\n%s", body)
	}
	if !strings.Contains(body, `"conversation_id":9`) {
		t.Errorf("the done event carries no conversation id, so the client cannot resume:\n%s", body)
	}
}

// TestChatStreamReportsEarlyFailuresAsJSON 钉住**起流之前**的失败仍走正常状态码。
//
// 这是懒起流的全部意义：响应头一旦发出就再也改不了状态码，而
// "会话不存在"这类失败在首字节之前就能判定，理应回 404 而不是 200 + 事件体。
func TestChatStreamReportsEarlyFailuresAsJSON(t *testing.T) {
	t.Parallel()

	engine := newEngine(t, &stubStore{convFound: false}, &stubLLM{})
	rec := postJSON(t, engine, "/api/v1/ai/chat/stream", `{"message":"问题","conversation_id":5}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Error("the stream was started before the failure was known, " +
			"so the status code could no longer be corrected")
	}
}

// TestChatStreamEndsWithDonePartialWhenInterrupted 钉住"断流但已保存"走 done。
//
// 客户端在这种情况下的处置与正常结束相同（拿 conversation_id 继续聊），
// 差别只是要不要提示"回答不完整"——因此它不该被表达成 error。
func TestChatStreamEndsWithDonePartialWhenInterrupted(t *testing.T) {
	t.Parallel()

	engine := newEngine(t, &stubStore{}, &stubLLM{
		deltas: []string{"已经生成"},
		err:    application.ErrUpstreamUnavailable,
	})

	rec := postJSON(t, engine, "/api/v1/ai/chat/stream", `{"message":"问题"}`)

	body := rec.Body.String()
	if !strings.Contains(body, "event: delta") {
		t.Fatalf("the delta the client already saw was not emitted:\n%s", body)
	}
	if !strings.Contains(body, "event: done") || !strings.Contains(body, `"partial":true`) {
		t.Errorf("an interrupted-but-persisted stream did not end with done/partial:\n%s", body)
	}
	if strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("an interrupted answer claims a normal finish reason:\n%s", body)
	}
}

// TestProvidersListsWhatIsEnabled 钉住厂商列表端点。
func TestProvidersListsWhatIsEnabled(t *testing.T) {
	t.Parallel()

	engine := newEngine(t, &stubStore{}, &stubLLM{})
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ai/providers", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var items []struct {
		Name    string `json:"name"`
		Model   string `json:"model"`
		Default bool   `json:"default"`
	}
	if err := json.Unmarshal(decode(t, rec).Data, &items); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(items) != 1 || items[0].Name != "deepseek" || !items[0].Default {
		t.Errorf("unexpected providers payload: %+v", items)
	}
}

// TestConversationRoutesRejectAMalformedID 钉住路径参数解析失败即 4xx。
//
// 不解析就直接往下传的话，一个 "abc" 会变成 0，而 0 在仓储层被当作
// "没有这个会话"——客户端得到 404，而真正的原因是它拼错了 URL。
func TestConversationRoutesRejectAMalformedID(t *testing.T) {
	t.Parallel()

	engine := newEngine(t, &stubStore{}, &stubLLM{})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ai/conversations/abc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET with a malformed id = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/ai/conversations/0", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("DELETE with a zero id = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestConversationDetailAndList 钉住两个读端点的正常路径。
func TestConversationDetailAndList(t *testing.T) {
	t.Parallel()

	conv := &models.AIConversation{
		ID: 5, UserID: 7, Title: "标题", MessageCount: 2,
	}
	engine := newEngine(t, &stubStore{conv: conv, convFound: true}, &stubLLM{})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ai/conversations", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"title":"标题"`) {
		t.Errorf("list = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ai/conversations/5", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role":"user"`) {
		t.Errorf("detail = %d %s", rec.Code, rec.Body.String())
	}
}

// TestRenameAndDeleteReportNotFound 钉住改不到/删不到的会话回 404。
func TestRenameAndDeleteReportNotFound(t *testing.T) {
	t.Parallel()

	engine := newEngine(t, &stubStore{renamed: false, deleted: false}, &stubLLM{})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/ai/conversations/5",
		strings.NewReader(`{"title":"新标题"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("PATCH = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/ai/conversations/5", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}
