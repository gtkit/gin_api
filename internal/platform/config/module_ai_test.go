package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func aiWithProviders(providers map[string]AIProviderConfig) AIModuleConfig {
	return AIModuleConfig{DefaultProvider: "deepseek", Providers: providers}
}

// TestAIEnabledFollowsTheApiKey 钉住"填了 api_key 才算启用"。
//
// 这条判据决定整个模块是否装配。判反的两个方向都坏：把没 key 的当启用，
// 每个请求都 503；把有 key 的当没启用，端点整个消失而配置看起来是好的。
func TestAIEnabledFollowsTheApiKey(t *testing.T) {
	t.Parallel()

	empty := aiWithProviders(map[string]AIProviderConfig{
		"deepseek": {Model: "deepseek-chat"},
		"qwen":     {APIKey: "   "}, // 只有空白也算没填
	})
	if empty.Enabled() {
		t.Error("a config whose keys are blank reported itself as enabled")
	}

	one := aiWithProviders(map[string]AIProviderConfig{
		"deepseek": {APIKey: "sk-x"},
		"qwen":     {},
	})
	if !one.Enabled() {
		t.Fatal("a config with one real key reported itself as disabled")
	}
	if got := one.EnabledProviders(); len(got) != 1 || got[0] != "deepseek" {
		t.Errorf("EnabledProviders() = %v, want only deepseek", got)
	}
}

// TestAIEnabledProvidersIsOrdered 钉住厂商名有序。
//
// map 迭代序随机；不排序的话"默认厂商是哪一家""启动日志里列了谁"
// 在两次启动之间都不一样，而那两处正是排查时要对照的。
func TestAIEnabledProvidersIsOrdered(t *testing.T) {
	t.Parallel()

	cfg := aiWithProviders(map[string]AIProviderConfig{
		"zhipu":    {APIKey: "k"},
		"deepseek": {APIKey: "k"},
		"qwen":     {APIKey: "k"},
	})
	got := cfg.EnabledProviders()
	want := []string{"deepseek", "qwen", "zhipu"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EnabledProviders() = %v, want %v", got, want)
		}
	}
}

// TestAIValidateAcceptsAConfigWithNoProvider 钉住"一家都没配"是合法状态。
//
// 随仓模板不含任何真实密钥，而模板必须能通过校验——否则每个新克隆
// 第一次启动就退出，而那与真正的配置错误无法区分。
func TestAIValidateAcceptsAConfigWithNoProvider(t *testing.T) {
	t.Parallel()

	cfg := aiWithProviders(map[string]AIProviderConfig{"deepseek": {}})
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Errorf("a template with no key failed validation: %v", errors.Join(errs...))
	}
}

// TestAIValidateRequiresAUsableDefaultProvider 钉住默认厂商必须指向已启用的一家。
//
// 留空或指错的后果是：每一个不带 provider 参数的请求都失败，
// 而配置看起来是完整的。
func TestAIValidateRequiresAUsableDefaultProvider(t *testing.T) {
	t.Parallel()

	missing := AIModuleConfig{Providers: map[string]AIProviderConfig{"qwen": {APIKey: "k"}}}
	if errs := missing.Validate(); len(errs) == 0 {
		t.Error("an enabled config without default_provider passed validation")
	}

	wrong := AIModuleConfig{
		DefaultProvider: "deepseek",
		Providers: map[string]AIProviderConfig{
			"deepseek": {}, // 没填 key，因此未启用
			"qwen":     {APIKey: "k"},
		},
	}
	errs := wrong.Validate()
	if len(errs) == 0 {
		t.Fatal("default_provider pointing at a provider without a key passed validation")
	}
	if !strings.Contains(errors.Join(errs...).Error(), "qwen") {
		t.Errorf("the error does not list what is actually enabled: %v", errors.Join(errs...))
	}
}

// TestAIValidateRejectsUpperCaseProviderKeys 钉住厂商名必须小写。
//
// 大小写不符的名字在装配期才失败，且报的是"未知厂商"——那条消息指不回
// 真正的原因（只是大小写）。在配置层直接说清楚。
func TestAIValidateRejectsUpperCaseProviderKeys(t *testing.T) {
	t.Parallel()

	cfg := AIModuleConfig{
		DefaultProvider: "DeepSeek",
		Providers:       map[string]AIProviderConfig{"DeepSeek": {APIKey: "k"}},
	}
	if errs := cfg.Validate(); len(errs) == 0 {
		t.Error("an upper-case provider key passed validation")
	}
}

// TestAIValidateRejectsAHeartbeatThatNeverFires 钉住心跳必须小于流的整体预算。
//
// 反过来的话，保活帧一次都发不出去，代理照样按空闲断连——而配置读起来
// 像是配了保活的。
func TestAIValidateRejectsAHeartbeatThatNeverFires(t *testing.T) {
	t.Parallel()

	cfg := aiWithProviders(map[string]AIProviderConfig{"deepseek": {APIKey: "k"}})
	cfg.StreamTotalTimeout = 10 * time.Second
	cfg.HeartbeatInterval = 30 * time.Second

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("a heartbeat longer than the stream budget passed validation")
	}
	if !strings.Contains(errors.Join(errs...).Error(), "heartbeat_interval") {
		t.Errorf("the error does not name the offending key: %v", errors.Join(errs...))
	}
}

// TestAIValidateBoundsEachKnob 钉住各旋钮的上下界。
func TestAIValidateBoundsEachKnob(t *testing.T) {
	t.Parallel()

	tooCold := float32(-1)
	tests := []struct {
		name   string
		mutate func(*AIModuleConfig)
	}{
		{"请求预算为负", func(c *AIModuleConfig) { c.RequestTimeout = -time.Second }},
		{"请求预算过短", func(c *AIModuleConfig) { c.RequestTimeout = time.Millisecond }},
		{"请求预算过长", func(c *AIModuleConfig) { c.RequestTimeout = time.Hour }},
		{"流式预算过长", func(c *AIModuleConfig) { c.StreamTotalTimeout = time.Hour }},
		{"心跳过密", func(c *AIModuleConfig) { c.HeartbeatInterval = time.Millisecond }},
		{"历史窗口过大", func(c *AIModuleConfig) { c.HistoryMessages = 10000 }},
		{"输出上限为负", func(c *AIModuleConfig) { c.MaxTokens = -1 }},
		{"输出上限过大", func(c *AIModuleConfig) { c.MaxTokens = 1 << 30 }},
		{"温度越界", func(c *AIModuleConfig) { c.Temperature = &tooCold }},
		{"重试次数为负", func(c *AIModuleConfig) { c.RetryAttempts = -1 }},
		{"重试次数过多", func(c *AIModuleConfig) { c.RetryAttempts = 50 }},
		{"熔断阈值为负", func(c *AIModuleConfig) { c.Breaker.ConsecutiveFailures = -1 }},
		{"熔断时长过长", func(c *AIModuleConfig) { c.Breaker.OpenTimeout = time.Hour }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := aiWithProviders(map[string]AIProviderConfig{"deepseek": {APIKey: "k"}})
			tt.mutate(&cfg)
			if errs := cfg.Validate(); len(errs) == 0 {
				t.Error("an out-of-range value passed validation")
			}
		})
	}
}

// TestAIEffectiveValuesFillInDefaults 钉住读取端补齐默认值。
//
// 补齐在读取端做而不是解码后就地改写：就地改写会让"配置文件里写了什么"
// 与"实际生效什么"在同一个结构体上分不开。
func TestAIEffectiveValuesFillInDefaults(t *testing.T) {
	t.Parallel()

	var zero AIModuleConfig
	if got := zero.EffectiveRequestTimeout(); got != aiDefaultRequestTimeout {
		t.Errorf("EffectiveRequestTimeout() = %s", got)
	}
	if got := zero.EffectiveStreamTotalTimeout(); got != aiDefaultStreamTotalTimeout {
		t.Errorf("EffectiveStreamTotalTimeout() = %s", got)
	}
	if got := zero.EffectiveHeartbeatInterval(); got != aiDefaultHeartbeatInterval {
		t.Errorf("EffectiveHeartbeatInterval() = %s", got)
	}
	if got := zero.EffectiveRetryAttempts(); got != aiDefaultRetryAttempts {
		t.Errorf("EffectiveRetryAttempts() = %d", got)
	}
	if got := zero.EffectiveBreakerFailures(); got != aiDefaultBreakerFailures {
		t.Errorf("EffectiveBreakerFailures() = %d", got)
	}
	if got := zero.EffectiveBreakerOpenTimeout(); got != aiDefaultBreakerOpenTimeout {
		t.Errorf("EffectiveBreakerOpenTimeout() = %s", got)
	}

	set := AIModuleConfig{RequestTimeout: time.Minute, RetryAttempts: 1}
	if got := set.EffectiveRequestTimeout(); got != time.Minute {
		t.Errorf("an explicit request_timeout was overridden: %s", got)
	}
	if got := set.EffectiveRetryAttempts(); got != 1 {
		t.Errorf("retry_attempts=1 (no retry) was overridden: %d", got)
	}
}

// TestAIHistoryWindowKeepsAnExplicitZero 钉住 history_messages=0 不被当成"没配"。
//
// 0 是有效取值（每轮独立问答）。退回默认等于配置里关掉的东西实际还开着，
// 而多出的那次查询与多付的输入 token 都无人知晓。
func TestAIHistoryWindowKeepsAnExplicitZero(t *testing.T) {
	t.Parallel()

	if got := (AIModuleConfig{HistoryMessages: 0}).EffectiveHistoryMessages(); got != 0 {
		t.Errorf("an explicit 0 window became %d", got)
	}
	if got := (AIModuleConfig{HistoryMessages: -1}).EffectiveHistoryMessages(); got != aiDefaultHistoryMessages {
		t.Errorf("a negative window became %d, want the default", got)
	}
	if got := (AIModuleConfig{HistoryMessages: 5}).EffectiveHistoryMessages(); got != 5 {
		t.Errorf("an explicit window became %d", got)
	}
}

// TestAIWarningsFireOnlyWhenNothingIsConfigured 钉住启动提示的触发条件。
//
// 一家厂商都没配的表现是"AI 端点整个不存在"，而那与"路由写错了"从外部
// 看一模一样。说出来才不至于让人去查路由。
func TestAIWarningsFireOnlyWhenNothingIsConfigured(t *testing.T) {
	t.Parallel()

	var cfg Config
	cfg.Modules.AI = aiWithProviders(map[string]AIProviderConfig{"deepseek": {}})
	if len(cfg.AIWarnings()) == 0 {
		t.Error("no warning was emitted although the AI endpoints will not exist")
	}

	cfg.Modules.AI = aiWithProviders(map[string]AIProviderConfig{"deepseek": {APIKey: "k"}})
	if got := cfg.AIWarnings(); len(got) != 0 {
		t.Errorf("a configured module still warned: %v", got)
	}

	var nilCfg *Config
	if got := nilCfg.AIWarnings(); got != nil {
		t.Errorf("AIWarnings on a nil config returned %v", got)
	}
}
