package ai

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	llm "github.com/gtkit/go-llm-provider/v2/provider"

	aiapp "github.com/gtkit/gin-api/internal/module/ai/application"
)

// fakeProvider 是 llm.Provider 的手写替身。
type fakeProvider struct {
	name llm.ProviderName

	// req 记录最后一次收到的请求，用于断言翻译结果。
	req *llm.ChatRequest

	resp *llm.ChatResponse
	err  error

	// chunks 是流式要吐出的片段；streamErr 在片段吐完后返回。
	chunks    []*llm.StreamChunk
	streamErr error
	// openErr 让 ChatStream 在建流阶段就失败。
	openErr error
	// closed 记录 StreamReader.Close 是否被调用。
	closed   bool
	closeErr error
}

func (f *fakeProvider) Name() llm.ProviderName { return f.name }

func (f *fakeProvider) Chat(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeProvider) ChatStream(_ context.Context, req *llm.ChatRequest) (*llm.StreamReader, error) {
	f.req = req
	if f.openErr != nil {
		return nil, f.openErr
	}

	i := 0
	return llm.NewStreamReader(func() (*llm.StreamChunk, error) {
		if i < len(f.chunks) {
			chunk := f.chunks[i]
			i++
			return chunk, nil
		}
		if f.streamErr != nil {
			return nil, f.streamErr
		}
		return nil, io.EOF
	}, func() error {
		f.closed = true
		return f.closeErr
	}), nil
}

func newClient(providers map[string]entry, def string) *llmClient {
	return &llmClient{providers: providers, defaultName: def, names: sortedNames(providers)}
}

// TestProvidersReportsTheEffectiveModel 钉住厂商列表带上生效模型与默认标记。
//
// 生效模型必须自己存一份：llm.Provider 接口只有 Name/Chat/ChatStream，
// 拿不到模型名，而落库与 /providers 都要报告真实取值。
func TestProvidersReportsTheEffectiveModel(t *testing.T) {
	t.Parallel()

	client := newClient(map[string]entry{
		"qwen":     {provider: &fakeProvider{name: "qwen"}, model: "qwen-max"},
		"deepseek": {provider: &fakeProvider{name: "deepseek"}, model: "deepseek-chat"},
	}, "qwen")

	got := client.Providers()
	if len(got) != 2 || got[0].Name != "deepseek" || got[1].Name != "qwen" {
		t.Fatalf("Providers() is not ordered: %+v", got)
	}
	if got[0].Model != "deepseek-chat" || got[1].Model != "qwen-max" {
		t.Errorf("effective models were not reported: %+v", got)
	}
	if got[0].Default || !got[1].Default {
		t.Errorf("the default flag is on the wrong provider: %+v", got)
	}
}

// TestResolveFallsBackToTheDefaultAndNamesTheAlternatives 钉住厂商解析。
func TestResolveFallsBackToTheDefaultAndNamesTheAlternatives(t *testing.T) {
	t.Parallel()

	client := newClient(map[string]entry{
		"deepseek": {provider: &fakeProvider{name: "deepseek"}, model: "deepseek-chat"},
	}, "deepseek")

	if _, err := client.resolve(""); err != nil {
		t.Fatalf("an empty provider name did not fall back to the default: %v", err)
	}

	_, err := client.resolve("nope")
	if !errors.Is(err, aiapp.ErrUnknownProvider) {
		t.Fatalf("resolve(unknown) = %v, want ErrUnknownProvider", err)
	}
	// 调用方多半只是把名字写错了；一句"不支持"不足以让它自己改对。
	if !strings.Contains(err.Error(), "deepseek") {
		t.Errorf("the error does not list the enabled providers: %v", err)
	}
}

// TestChatTranslatesRequestAndResponse 钉住中立类型与 SDK 类型的双向翻译。
func TestChatTranslatesRequestAndResponse(t *testing.T) {
	t.Parallel()

	temp := float32(0.3)
	fake := &fakeProvider{
		name: "deepseek",
		resp: &llm.ChatResponse{
			Content: "回答", Reasoning: "思考", FinishReason: "stop",
			Usage: llm.Usage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12},
		},
	}
	client := newClient(map[string]entry{
		"deepseek": {provider: fake, model: "deepseek-chat"},
	}, "deepseek")

	reply, err := client.Chat(context.Background(), aiapp.ChatCall{
		Messages: []aiapp.ChatMessage{
			{Role: aiapp.RoleSystem, Content: "你是助手"},
			{Role: aiapp.RoleUser, Content: "问题"},
		},
		MaxTokens:   128,
		Temperature: &temp,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if len(fake.req.Messages) != 2 || fake.req.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("messages were not translated: %+v", fake.req.Messages)
	}
	if len(fake.req.Messages[1].Content) != 1 ||
		fake.req.Messages[1].Content[0].Text != "问题" {
		t.Errorf("text content part was not built: %+v", fake.req.Messages[1].Content)
	}
	if fake.req.MaxTokens != 128 || fake.req.Temperature == nil || *fake.req.Temperature != 0.3 {
		t.Errorf("sampling parameters were dropped: %+v", fake.req)
	}
	// req.Model 必须留空：显式指定会覆盖构造时配的默认模型，
	// 也会让降级链上的各成员全用同一个模型名。
	if fake.req.Model != "" {
		t.Errorf("Model was set on the request although the caller left it empty: %q", fake.req.Model)
	}

	if reply.Content != "回答" || reply.Reasoning != "思考" || reply.FinishReason != "stop" {
		t.Errorf("response was not translated: %+v", reply)
	}
	if reply.Provider != "deepseek" || reply.Model != "deepseek-chat" {
		t.Errorf("the effective provider/model was not reported: %+v", reply)
	}
	if reply.Usage.TotalTokens != 12 {
		t.Errorf("usage was not translated: %+v", reply.Usage)
	}
}

// TestChatUsesTheRequestedModelWhenGiven 钉住显式模型覆盖默认值。
func TestChatUsesTheRequestedModelWhenGiven(t *testing.T) {
	t.Parallel()

	fake := &fakeProvider{name: "deepseek", resp: &llm.ChatResponse{Content: "答"}}
	client := newClient(map[string]entry{
		"deepseek": {provider: fake, model: "deepseek-chat"},
	}, "deepseek")

	reply, err := client.Chat(context.Background(), aiapp.ChatCall{Model: "deepseek-reasoner"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if fake.req.Model != "deepseek-reasoner" {
		t.Errorf("the requested model did not reach the provider: %q", fake.req.Model)
	}
	if reply.Model != "deepseek-reasoner" {
		t.Errorf("the reported model = %q, want the requested one", reply.Model)
	}
}

// TestStreamAccumulatesDeltasAndTailUsage 钉住流式聚合。
//
// 用量随流尾部的 chunk 到达，且可能出现在 FinishReason **之后**、io.EOF 之前
// （OpenAI 兼容端点就是这样）。只看带 FinishReason 的那个 chunk 会漏掉它，
// 表现为"每次对话的 token 都是 0"。
func TestStreamAccumulatesDeltasAndTailUsage(t *testing.T) {
	t.Parallel()

	fake := &fakeProvider{
		name: "deepseek",
		chunks: []*llm.StreamChunk{
			{Delta: "你", ReasoningDelta: "想"},
			nil, // 真实实现偶有空 chunk，不能因此 panic
			{Delta: "好", Model: "deepseek-chat"},
			{FinishReason: "stop"},
			{Usage: llm.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}},
		},
	}
	client := newClient(map[string]entry{
		"deepseek": {provider: fake, model: "配置里的默认"},
	}, "deepseek")

	var seen []string
	reply, err := client.Stream(context.Background(), aiapp.ChatCall{},
		func(d string) error { seen = append(seen, d); return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if strings.Join(seen, "") != "你好" || reply.Content != "你好" {
		t.Errorf("deltas = %q, content = %q", strings.Join(seen, ""), reply.Content)
	}
	if reply.Reasoning != "想" {
		t.Errorf("reasoning deltas were dropped: %q", reply.Reasoning)
	}
	if reply.FinishReason != "stop" {
		t.Errorf("finish reason = %q", reply.FinishReason)
	}
	if reply.Usage.TotalTokens != 5 {
		t.Errorf("tail usage was dropped: %+v", reply.Usage)
	}
	// 响应侧回传的模型名优先：请求留空时它才是真实取值。
	if reply.Model != "deepseek-chat" {
		t.Errorf("model = %q, want the one the response reported", reply.Model)
	}
	if !fake.closed {
		t.Error("the stream reader was not closed: the connection would leak")
	}
}

// TestStreamStopsReadingWhenTheClientIsGone 钉住写回失败即停止读上游。
//
// 继续读没有意义，只是白付剩余 token 的钱——而那部分内容没有任何人会看到。
func TestStreamStopsReadingWhenTheClientIsGone(t *testing.T) {
	t.Parallel()

	fake := &fakeProvider{
		name:   "deepseek",
		chunks: []*llm.StreamChunk{{Delta: "一"}, {Delta: "二"}, {Delta: "三"}},
	}
	client := newClient(map[string]entry{
		"deepseek": {provider: fake, model: "m"},
	}, "deepseek")

	gone := errors.New("client gone")
	var count int
	reply, err := client.Stream(context.Background(), aiapp.ChatCall{}, func(string) error {
		count++
		if count == 2 {
			return gone
		}
		return nil
	})

	if !errors.Is(err, gone) {
		t.Fatalf("Stream = %v, want the callback error to surface", err)
	}
	if count != 2 {
		t.Errorf("the reader kept pulling chunks after the client was gone (%d callbacks)", count)
	}
	// 已经写出去的那部分必须带回来：上层要靠它落库。
	if reply.Content != "一二" {
		t.Errorf("partial content = %q, want everything already emitted", reply.Content)
	}
}

// TestStreamReturnsPartialContentOnUpstreamFailure 钉住上游中断时带回已生成部分。
func TestStreamReturnsPartialContentOnUpstreamFailure(t *testing.T) {
	t.Parallel()

	fake := &fakeProvider{
		name:      "deepseek",
		chunks:    []*llm.StreamChunk{{Delta: "已经生成"}},
		streamErr: &llm.ProviderError{Provider: "deepseek", Code: llm.ErrorCodeServerError},
	}
	client := newClient(map[string]entry{
		"deepseek": {provider: fake, model: "m"},
	}, "deepseek")

	reply, err := client.Stream(context.Background(), aiapp.ChatCall{}, func(string) error { return nil })
	if !errors.Is(err, aiapp.ErrUpstreamUnavailable) {
		t.Fatalf("Stream = %v, want a translated upstream failure", err)
	}
	if reply.Content != "已经生成" {
		t.Errorf("partial content = %q: what the user already saw would be lost", reply.Content)
	}
}

// TestTranslateErrorMapsEveryProviderCode 钉住错误分类表。
//
// 这一层是边界的价值所在：application 因此既不 import SDK，也不按错误文本
// 判断——各厂商的文案不一致且随时会变，按文本匹配的分支会在某次上游文案
// 调整后静默失效。
func TestTranslateErrorMapsEveryProviderCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code llm.ErrorCode
		want error
	}{
		{llm.ErrorCodeRateLimit, aiapp.ErrUpstreamRateLimited},
		{llm.ErrorCodeContextLength, aiapp.ErrContextTooLong},
		{llm.ErrorCodeContentFilter, aiapp.ErrContentFiltered},
		{llm.ErrorCodeAuth, aiapp.ErrUpstreamAuth},
		{llm.ErrorCodeInvalidRequest, aiapp.ErrUpstreamUnavailable},
		{llm.ErrorCodeTimeout, aiapp.ErrUpstreamUnavailable},
		{llm.ErrorCodeServerError, aiapp.ErrUpstreamUnavailable},
		{llm.ErrorCodeNetwork, aiapp.ErrUpstreamUnavailable},
		{llm.ErrorCodeUnknown, aiapp.ErrUpstreamUnavailable},
		{llm.ErrorCode("something-new"), aiapp.ErrUpstreamUnavailable},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			t.Parallel()

			got := translateError(&llm.ProviderError{Provider: "deepseek", Code: tt.code})
			if !errors.Is(got, tt.want) {
				t.Errorf("translateError(%s) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

// TestTranslateErrorKeepsContextErrorsIntact 钉住 ctx 错误原样上抛。
//
// 上层要用 errors.Is(err, context.Canceled) 区分"客户端主动断开"与真正的
// 故障。包成领域错误会让那个判断失效，于是每一次用户按下停止都会被记成
// 一次上游故障，把错误率与告警一起污染。
func TestTranslateErrorKeepsContextErrorsIntact(t *testing.T) {
	t.Parallel()

	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		got := translateError(err)
		if !errors.Is(got, err) {
			t.Errorf("translateError(%v) lost the context error: %v", err, got)
		}
		if errors.Is(got, aiapp.ErrUpstreamUnavailable) {
			t.Errorf("translateError(%v) disguised a cancellation as an upstream failure", err)
		}
	}

	if translateError(nil) != nil {
		t.Error("translateError(nil) returned an error")
	}
}

// TestTranslateErrorWrapsUnstructuredFailures 钉住裸 error 落到"上游不可用"。
//
// 那是唯一不会误导调用方的兜底：它意味着"重试可能有用"，而把未知错误
// 说成"你的请求有问题"会让调用方去改一个没问题的请求。
func TestTranslateErrorWrapsUnstructuredFailures(t *testing.T) {
	t.Parallel()

	raw := errors.New("dial tcp: i/o timeout")
	got := translateError(raw)
	if !errors.Is(got, aiapp.ErrUpstreamUnavailable) || !errors.Is(got, raw) {
		t.Errorf("translateError(raw) = %v, want the raw cause wrapped in ErrUpstreamUnavailable", got)
	}
}

// TestStreamReportsAFailureToOpen 钉住建流阶段的失败也走翻译。
func TestStreamReportsAFailureToOpen(t *testing.T) {
	t.Parallel()

	client := newClient(map[string]entry{
		"deepseek": {
			provider: &fakeProvider{
				name:    "deepseek",
				openErr: &llm.ProviderError{Provider: "deepseek", Code: llm.ErrorCodeAuth},
			},
			model: "m",
		},
	}, "deepseek")

	_, err := client.Stream(context.Background(), aiapp.ChatCall{}, func(string) error { return nil })
	if !errors.Is(err, aiapp.ErrUpstreamAuth) {
		t.Errorf("Stream = %v, want ErrUpstreamAuth", err)
	}
}

// TestLogAuditorToleratesAMissingSink 钉住未注入日志出口时不 panic。
func TestLogAuditorToleratesAMissingSink(t *testing.T) {
	t.Parallel()

	logAuditor{}.Auditf("no sink %d", 1)

	var got string
	logAuditor{logf: func(format string, args ...any) { got = format }}.Auditf("hit %d", 1)
	if got != "hit %d" {
		t.Errorf("the auditor did not forward to the sink: %q", got)
	}
}

// Cleanup failures remain visible after EOF and beside receive/callback failures.
func TestStreamRetainsCloseFailureAndPartialReply(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("close failed")
	primary := errors.New("primary failed")
	for _, mode := range []string{"eof", "receive", "callback"} {
		t.Run(mode, func(t *testing.T) {
			fake := &fakeProvider{name: "deepseek", chunks: []*llm.StreamChunk{{Delta: "partial"}}, closeErr: closeErr}
			if mode == "receive" {
				fake.streamErr = primary
			}
			client := newClient(map[string]entry{"deepseek": {provider: fake, model: "m"}}, "deepseek")
			reply, err := client.Stream(t.Context(), aiapp.ChatCall{}, func(string) error {
				if mode == "callback" {
					return primary
				}
				return nil
			})
			if !fake.closed || reply.Content != "partial" || !errors.Is(err, closeErr) {
				t.Fatalf("closed=%v reply=%+v err=%v", fake.closed, reply, err)
			}
			if mode != "eof" && !errors.Is(err, primary) {
				t.Fatalf("lost primary failure: %v", err)
			}
		})
	}
}
