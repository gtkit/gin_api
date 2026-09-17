package ai

import (
	"reflect"
	"strings"
	"testing"
	"time"

	llm "github.com/gtkit/go-llm-provider/v2/provider"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func enabledConfig(providers map[string]config.AIProviderConfig, def string) config.AIModuleConfig {
	return config.AIModuleConfig{DefaultProvider: def, Providers: providers}
}

// TestNewRefusesToAssembleWithoutAProvider 钉住装配入口的前置条件。
//
// 它由调用方（bootstrap）按 cfg.Enabled() 守卫；这里兜底是因为一个"没有任何
// 上游"的 service 只会让每个请求都失败，而那比启动失败更难定位。
func TestNewRefusesToAssembleWithoutAProvider(t *testing.T) {
	t.Parallel()

	_, err := New(Deps{Config: enabledConfig(map[string]config.AIProviderConfig{
		"deepseek": {},
	}, "deepseek")})
	if err == nil {
		t.Fatal("New assembled a module with no enabled provider")
	}
	if !strings.Contains(err.Error(), "Enabled") {
		t.Errorf("the error does not point at the guard: %v", err)
	}
}

// TestNewRequiresADatabase 钉住缺库即装配失败。
func TestNewRequiresADatabase(t *testing.T) {
	t.Parallel()

	_, err := New(Deps{Config: enabledConfig(map[string]config.AIProviderConfig{
		"deepseek": {APIKey: "sk-test"},
	}, "deepseek")})
	if err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("New without a database = %v, want a database error", err)
	}
}

// TestBuildClientFailsOnAnUnknownProviderName 钉住拼错的厂商名让启动失败。
//
// 静默跳过的后果是"配了却不生效"，而那与"key 填错了"从外部完全无法区分——
// 运维会去换密钥，而真正的问题只是名字写错了一个字母。
func TestBuildClientFailsOnAnUnknownProviderName(t *testing.T) {
	t.Parallel()

	_, err := buildClient(enabledConfig(map[string]config.AIProviderConfig{
		"deepsek": {APIKey: "sk-test"}, // 少一个 e
	}, "deepsek"))
	if err == nil {
		t.Fatal("an unknown provider name was silently accepted")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("the error does not name the problem: %v", err)
	}
	// 报错必须列出可选集合，否则使用者只知道"不行"，不知道该写什么。
	if !strings.Contains(err.Error(), "deepseek") {
		t.Errorf("the error does not list the known providers: %v", err)
	}
}

// TestBuildClientResolvesPresetsAndOverrides 钉住模型/地址的取值优先级。
func TestBuildClientResolvesPresetsAndOverrides(t *testing.T) {
	t.Parallel()

	client, err := buildClient(enabledConfig(map[string]config.AIProviderConfig{
		// 留空 → 取预设默认模型。
		"deepseek": {APIKey: "sk-test"},
		// 显式指定 → 覆盖预设。
		"qwen": {APIKey: "sk-test", Model: "qwen-max", BaseURL: "https://gateway.internal/v1"},
	}, "deepseek"))
	if err != nil {
		t.Fatalf("buildClient: %v", err)
	}

	presetModel := llm.AllPresets()[llm.ProviderDeepSeek].DefaultModel
	if got := client.providers["deepseek"].model; got != presetModel {
		t.Errorf("deepseek model = %q, want the preset default %q", got, presetModel)
	}
	if got := client.providers["qwen"].model; got != "qwen-max" {
		t.Errorf("qwen model = %q, want the configured override", got)
	}
	if client.defaultName != "deepseek" {
		t.Errorf("default provider = %q", client.defaultName)
	}
	if len(client.names) != 2 || client.names[0] != "deepseek" || client.names[1] != "qwen" {
		t.Errorf("provider names are not ordered: %v", client.names)
	}
}

// TestBuildClientRejectsADefaultThatIsNotEnabled 钉住装配层的兜底判定。
//
// 配置校验已经拦过这一条；本函数也可能被直接调用，而一个指不到任何 provider
// 的默认名会让每个不带 provider 的请求都失败。
func TestBuildClientRejectsADefaultThatIsNotEnabled(t *testing.T) {
	t.Parallel()

	_, err := buildClient(enabledConfig(map[string]config.AIProviderConfig{
		"deepseek": {APIKey: "sk-test"},
	}, "qwen"))
	if err == nil || !strings.Contains(err.Error(), "default provider") {
		t.Fatalf("buildClient with an unusable default = %v", err)
	}
}

// TestHardenIsResilientToBothKnobs 钉住重试与熔断的装配都不返回"每次都报错"的假 provider。
//
// 库的 WithRetry / WithBreaker 在输入非法时返回一个恒失败的 provider，
// 于是装配错误变成运行期错误，且发生在**每一个**请求上。这里用 Try* 版本，
// 装配阶段就该定局。
func TestHardenIsResilientToBothKnobs(t *testing.T) {
	t.Parallel()

	base := &fakeProvider{name: "deepseek", resp: &llm.ChatResponse{Content: "ok"}}

	// retry_attempts <= 1 表示不重试，此时不该套重试装饰器。
	noRetry := config.AIModuleConfig{RetryAttempts: 1}
	if _, err := harden(base, "deepseek", noRetry); err != nil {
		t.Fatalf("harden without retry: %v", err)
	}

	withRetry := config.AIModuleConfig{
		RetryAttempts: 3,
		Breaker:       config.AIBreakerConfig{ConsecutiveFailures: 2, OpenTimeout: time.Second},
	}
	if _, err := harden(base, "deepseek", withRetry); err != nil {
		t.Fatalf("harden with retry: %v", err)
	}
}

// TestNewProviderPicksTheNativePathForNonOpenAIVendors 钉住协议分派。
//
// 用 OpenAI 兼容路径构造 Claude / Gemini 会得到一个能启动、能发请求、但每次
// 都被对端拒绝的 provider，而错误信息指向的是"上游返回了 404"。判据是
// **构造出来的类型不同**——同一个类型说明分派没生效。
func TestNewProviderPicksTheNativePathForNonOpenAIVendors(t *testing.T) {
	t.Parallel()

	presets := llm.AllPresets()

	openaiCompatible, err := newProvider(llm.ProviderDeepSeek, "k",
		presets[llm.ProviderDeepSeek].BaseURL, presets[llm.ProviderDeepSeek].DefaultModel)
	if err != nil {
		t.Fatalf("newProvider(deepseek): %v", err)
	}

	native, err := newProvider(llm.ProviderAnthropic, "k",
		presets[llm.ProviderAnthropic].BaseURL, presets[llm.ProviderAnthropic].DefaultModel)
	if err != nil {
		t.Fatalf("newProvider(anthropic): %v", err)
	}

	if got, want := native.Name(), llm.ProviderAnthropic; got != want {
		t.Errorf("native provider name = %q, want %q", got, want)
	}
	// 类型相同即说明 anthropic 被当成了 OpenAI 兼容平台。
	if typeName(openaiCompatible) == typeName(native) {
		t.Errorf("anthropic was built through the OpenAI-compatible path (%s): "+
			"every request would be rejected by the endpoint",
			typeName(native))
	}
}

// typeName 返回动态类型名，用于区分"走了哪条协议路径"。
func typeName(v any) string {
	return reflect.TypeOf(v).String()
}
