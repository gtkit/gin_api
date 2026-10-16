// Package ai 是 ai 对话模块的装配层：把配置与数据库翻译成 application 需要的
// port 实现（LLM 客户端 + 聚合仓储），再注入 application service。
//
// 装配层的红线（docs/architecture.md）在这里同样适用：不承载业务规则、
// 不按环境名推导行为。厂商名是否被 LLM 库认识、默认厂商是否已启用，
// 都在本层判定并让启动失败——配置包不 import SDK，那些判断只能落在这里。
package ai

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	llm "github.com/gtkit/go-llm-provider/v2/provider"
	logger "github.com/gtkit/logger/v2"
	"gorm.io/gorm"

	aiapp "github.com/gtkit/gin-api/internal/module/ai/application"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/repository/aiconversation"
)

// Deps 是装配本模块所需的进程级依赖。
type Deps struct {
	DB     *gorm.DB
	Config config.AIModuleConfig
}

// Assembly 是装配产物。
//
// 用 Deps/Assembly 而不是直接返回 service：本模块的装配入参已经有两项，
// 且可预见会增加（指标、限流、计费 hook）。位置参数增到第四五个之后，
// 调用点就再也读不出哪个是哪个，而类型相同的两个参数写反不会有编译错误。
type Assembly struct {
	Service *aiapp.Service
	// Close 释放本模块自建的客户端，由 bootstrap 接入回滚与关停。
	Close func() error
	// Timeouts 是 transport 层需要的两个预算与心跳间隔。
	//
	// 由装配层交出来而不是让 transport 自己读配置：transport 拿到的应该是
	// 已经补齐默认值的**生效值**，而不是原始配置——两处各自补齐会分叉。
	Timeouts Timeouts
}

// Timeouts 是 transport 层要用的生效时长。
type Timeouts struct {
	// Request 是非流式对话的总预算。
	Request time.Duration
	// StreamTotal 是流式对话从起流到收尾的整体预算。
	StreamTotal time.Duration
	// Heartbeat 是 SSE 保活帧间隔。
	Heartbeat time.Duration
}

// New 装配 ai 对话模块。
//
// 只在 cfg.Enabled()（至少一家厂商填了密钥）时调用：没有可用上游时整个模块
// 不存在，而不是注册一组永远 503 的端点。
//
// 返回 error 而不是 panic：装配缺失是启动期错误，应当以错误表达并一路上抛。
func New(deps Deps) (Assembly, error) {
	cfg := deps.Config

	if !cfg.Enabled() {
		return Assembly{}, fmt.Errorf(
			"ai: New called with no provider enabled; guard the call with config.AIModuleConfig.Enabled")
	}
	if deps.DB == nil {
		return Assembly{}, fmt.Errorf("ai: database is required")
	}

	client, err := buildClient(cfg)
	if err != nil {
		return Assembly{}, err
	}

	service, err := aiapp.NewService(
		aiapp.Deps{
			Store: aiconversation.New(deps.DB),
			LLM:   client,
		},
		aiapp.WithAuditor(logAuditor{logf: logger.Infof}),
		aiapp.WithSystemPrompt(cfg.SystemPrompt),
		aiapp.WithHistoryMessages(cfg.EffectiveHistoryMessages()),
		aiapp.WithMaxTokens(cfg.MaxTokens),
		aiapp.WithTemperature(cfg.Temperature),
	)
	if err != nil {
		_ = client.Close()
		return Assembly{}, fmt.Errorf("ai: build service: %w", err)
	}

	logger.Infof("ai chat assembled with providers %v (default=%s)", client.names, client.defaultName)

	return Assembly{
		Service: service,
		Close:   client.Close,
		Timeouts: Timeouts{
			Request:     cfg.EffectiveRequestTimeout(),
			StreamTotal: cfg.EffectiveStreamTotalTimeout(),
			Heartbeat:   cfg.EffectiveHeartbeatInterval(),
		},
	}, nil
}

// buildClient 按配置逐家构造 provider，并包上重试与熔断。
func buildClient(cfg config.AIModuleConfig) (*llmClient, error) {
	return buildClientWithFactory(cfg, newHTTPClient)
}

func buildClientWithFactory(cfg config.AIModuleConfig, create func() *http.Client) (_ *llmClient, err error) {
	var clients []*http.Client
	closeClients := sync.OnceValue(func() error {
		for _, client := range slices.Backward(clients) {
			client.CloseIdleConnections()
		}
		return nil
	})
	defer func() {
		if err != nil {
			_ = closeClients()
		}
	}()
	presets := llm.AllPresets()
	enabled := cfg.EnabledProviders()

	providers := make(map[string]entry, len(enabled))
	for _, name := range enabled {
		pc := cfg.Providers[name]

		preset, known := presets[llm.ProviderName(name)]
		if !known {
			// 名字不认识就失败，不跳过：静默跳过会让一个拼错的厂商名表现为
			// "配了却不生效"，而那与"key 填错了"从外部完全无法区分。
			return nil, fmt.Errorf(
				"ai: unknown provider %q in modules.ai.providers; known providers: %v",
				name, slices.Sorted(maps.Keys(presets)))
		}

		model := pc.Model
		if model == "" {
			model = preset.DefaultModel
		}
		baseURL := pc.BaseURL
		if baseURL == "" {
			baseURL = preset.BaseURL
		}

		client := create()
		clients = append(clients, client)
		base, err := newProvider(llm.ProviderName(name), pc.APIKey, baseURL, model, client)
		if err != nil {
			return nil, fmt.Errorf("ai: build provider %q: %w", name, err)
		}

		resilient, err := harden(base, name, cfg)
		if err != nil {
			return nil, fmt.Errorf("ai: harden provider %q: %w", name, err)
		}

		providers[name] = entry{provider: resilient, model: model}
	}

	defaultName := cfg.DefaultProvider
	if _, ok := providers[defaultName]; !ok {
		// 配置校验已经拦过这一条；这里兜底是因为本函数也可能被直接调用，
		// 而一个指不到任何 provider 的默认名会让每个不带 provider 的请求都失败。
		return nil, fmt.Errorf(
			"ai: default provider %q is not enabled (enabled: %v)", defaultName, sortedNames(providers))
	}

	return &llmClient{
		providers:   providers,
		defaultName: defaultName,
		names:       sortedNames(providers),
		close:       closeClients,
	}, nil
}

// newProvider 按协议形态构造一家厂商的 Provider。
//
// 不用库的 NewProviderFromPreset：它不接受自定义 HTTP 客户端，会各自新建一个
// 库内默认 client。这里要注入本仓自己的传输参数（见 httpclient.go）。
//
// switch 分支不能省，即便当前配置的都是 OpenAI 兼容平台：Claude 与 Gemini 走
// **各自的原生协议**，用 OpenAI 兼容路径构造它们会得到一个能启动、能发请求、
// 但每次都被对端拒绝的 provider——而错误信息指向的是"上游返回了 404"。
func newProvider(name llm.ProviderName, apiKey, baseURL, model string, client *http.Client) (llm.Provider, error) {
	switch name {
	case llm.ProviderAnthropic:
		return llm.NewAnthropicProvider(llm.NativeProviderConfig{
			APIKey: apiKey, BaseURL: baseURL, Model: model, HTTPClient: client,
		})
	case llm.ProviderGemini:
		return llm.NewGeminiProvider(llm.NativeProviderConfig{
			APIKey: apiKey, BaseURL: baseURL, Model: model, HTTPClient: client,
		})
	case llm.ProviderOllama:
		// 本地推理不需要密钥；本仓用非空 api_key 作为"启用"开关，
		// 因此接入 Ollama 时填任意占位值即可，这里不把它传下去。
		return llm.NewOllamaProvider(llm.OllamaProviderConfig{
			BaseURL: baseURL, Model: model, HTTPClient: client,
		})
	default:
		return llm.NewProvider(llm.ProviderConfig{
			Name: name, BaseURL: baseURL, APIKey: apiKey, Model: model, HTTPClient: client,
		})
	}
}

// harden 给 provider 包上重试与熔断。
//
// 熔断在外、重试在内：整轮请求只统计一次最终结果，避免单次请求的
// 多次尝试消耗掉整个失败阈值。已经熔断时不进入重试与退避。
//
// 用 TryWithRetry / TryWithBreaker 而不是 WithRetry / WithBreaker：后者在输入
// 非法时返回一个"每次调用都报同一个错"的 provider，于是装配错误变成运行期
// 错误，且发生在每一个请求上。
func harden(p llm.Provider, name string, cfg config.AIModuleConfig) (llm.Provider, error) {
	breaker := llm.NewBreaker(llm.BreakerOptions{
		Name:             name,
		FailureThreshold: cfg.EffectiveBreakerFailures(),
		OpenDuration:     cfg.EffectiveBreakerOpenTimeout(),
	})
	attempts := cfg.EffectiveRetryAttempts()
	if attempts <= 1 {
		return llm.TryWithBreaker(p, breaker)
	}

	// 全抖动退避：不加抖动时，一批同时失败的请求会在同一时刻一起重试，
	// 把刚缓过来的上游再次打垮。
	retried, err := llm.TryWithRetry(p, llm.RetryOptions{
		MaxAttempts: attempts,
		Backoff:     llm.ExponentialBackoffWithJitter(200*time.Millisecond, 2*time.Second),
	})
	if err != nil {
		return nil, fmt.Errorf("attach retry: %w", err)
	}
	guarded, err := llm.TryWithBreaker(retried, breaker)
	if err != nil {
		return nil, fmt.Errorf("attach breaker: %w", err)
	}
	return guarded, nil
}
