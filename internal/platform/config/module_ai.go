package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// AIModuleConfig 是 ai 模块的配置子树。
//
// 厂商集合由 Providers 这张表驱动：**填了 api_key 的条目才启用**。这样接入一家
// 新厂商只是配置里多一段，不需要改代码；停用一家只是把 key 清空，不需要删配置段
// （删掉的话，下次要恢复得先回去查它叫什么名字）。
//
// 一家都没启用时整个模块不装配（Enabled 为假），路由不注册、不建任何出站客户端。
// 这是有意的：随仓模板不含任何真实密钥，而"注册一组永远报 503 的端点"比"端点
// 不存在"更难排查——前者看起来是上游挂了，后者一眼就是没配。
type AIModuleConfig struct {
	// DefaultProvider 是请求未指定厂商时使用的厂商名。
	//
	// 启用了任意厂商就必须填，且必须指向一个**已启用**的厂商：留空或指向一个
	// 没有 key 的厂商，会让每一个不带 provider 参数的请求都失败，而配置看起来
	// 是完整的。
	DefaultProvider string `mapstructure:"default_provider"`

	// RequestTimeout 是**非流式**单次对话的总预算，经 ctx 下传。
	//
	// 不靠 HTTP 客户端的 Timeout：那个字段对流式响应意味着"读满这么久就掐断"，
	// 与"等首字节多久算超时"是两回事。预算统一走 ctx，两种调用形态才有一致语义。
	RequestTimeout time.Duration `mapstructure:"request_timeout"`

	// StreamTotalTimeout 是**流式**对话从起流到收尾的整体预算。
	//
	// 必须显著大于 RequestTimeout：长回答逐 token 吐出来本就慢，用同一个数字
	// 会让流在正常生成过程中被掐断，而客户端只看到回答戛然而止。
	StreamTotalTimeout time.Duration `mapstructure:"stream_total_timeout"`

	// HeartbeatInterval 是 SSE 保活注释帧的间隔。
	//
	// 必须有：模型在思考期间可能几十秒不吐字，而中间代理按"空闲连接"断开时
	// 不会有任何错误信息，表现为前端莫名其妙地断流。
	HeartbeatInterval time.Duration `mapstructure:"heartbeat_interval"`

	// HistoryMessages 是组装上下文时回溯的**最大历史消息条数**（不含本轮提问）。
	//
	// 按条数而不是按 token：token 数要调用方各自实现分词才能估，而估错的方向
	// 通常是"以为没超"。条数是确定的，配合模型的上下文窗口留足余量即可。
	// 0 表示不带历史（每轮都是独立问答）。
	HistoryMessages int `mapstructure:"history_messages"`

	// MaxTokens 是单次回复的输出上限；<=0 表示不下发该参数，由厂商取默认。
	MaxTokens int `mapstructure:"max_tokens"`

	// Temperature 是采样温度；留空表示不下发该参数，由厂商取默认。
	//
	// 用指针而不是 float32：0 是一个**有意义的取值**（尽可能确定的输出），
	// 与"没配"必须能区分开，而零值判定做不到这一点。
	Temperature *float32 `mapstructure:"temperature"`

	// SystemPrompt 是拼在每个会话最前面的系统提示词；留空表示不加。
	SystemPrompt string `mapstructure:"system_prompt"`

	// RetryAttempts 是单次调用的**总尝试次数**（1 表示不重试）。
	//
	// 重试是 at-least-once：厂商已处理但响应在回程丢失时，重试会真实地再调一次，
	// 产生重复计费。这里默认保守，成本敏感的部署应当显式调低。
	RetryAttempts int `mapstructure:"retry_attempts"`

	// Breaker 是各厂商共用的熔断阈值；实例按厂商名各自独立。
	//
	// 阈值共用而实例独立，与 outbound 的口径一致：一家厂商故障只熔断它自己，
	// 而"多少次连续失败算故障"是运维口径，按厂商拆散只会让每接一家就多出
	// 一组没人知道该填什么的配置项。
	Breaker AIBreakerConfig `mapstructure:"breaker"`

	// Providers 按厂商名声明接入参数。键必须是库认识的厂商标识
	//（deepseek / qwen / zhipu / moonshot / ark / qianfan / siliconflow ...）。
	//
	// **键的合法性不在本包校验**：platform/config 是基础设施层，不该为了认识
	// 一串厂商名而 import LLM SDK——那会让依赖方向倒转，且 SDK 每加一家厂商
	// 就要改一次配置包。名字是否被库认识由 runtime/module/ai 在装配期判定，
	// 不认识即启动失败。
	Providers map[string]AIProviderConfig `mapstructure:"providers"`
}

// AIProviderConfig 是单家厂商的接入参数。
type AIProviderConfig struct {
	// APIKey 是该厂商的密钥；**留空即不启用这一家**。
	APIKey string `mapstructure:"api_key"`
	// Model 是默认模型；留空取库内预设的该厂商推荐模型。
	Model string `mapstructure:"model"`
	// BaseURL 覆盖库内预设地址；留空取预设。
	//
	// 存在的理由是私有网关与专有云端点：那些地址不可能被预设覆盖，
	// 而没有这个旋钮时接入方只能改代码。
	BaseURL string `mapstructure:"base_url"`
}

// Enabled 报告该厂商是否启用（填了密钥）。
func (c AIProviderConfig) Enabled() bool {
	return strings.TrimSpace(c.APIKey) != ""
}

// AIBreakerConfig 是 LLM 出站熔断的阈值。
type AIBreakerConfig struct {
	// ConsecutiveFailures 连续失败请求达到该值即熔断；整轮重试计一次，<=0 取默认（5）。
	ConsecutiveFailures int `mapstructure:"consecutive_failures"`
	// OpenTimeout 是熔断后到半开探测的等待；<=0 取默认（60s）。
	OpenTimeout time.Duration `mapstructure:"open_timeout"`
}

// ai 模块各项未配置时的兜底值。
//
// 取值理由：
//
//	requestTimeout      60s —— 主流模型一次完整回答的 P99 量级；再长的等待
//	                    对同步接口没有意义，调用方早已放弃。
//	streamTotalTimeout  5m  —— 流式的长回答（长文改写、代码生成）确实能到分钟级，
//	                    但它必须有界：无界的流会把 goroutine 与连接一起长期扣住。
//	heartbeatInterval   15s —— 常见反向代理的空闲断连阈值多在 30～60s，
//	                    取其一半留出一次丢帧的余量。
//	historyMessages     20  —— 约十轮对话。再多的历史对回答质量的边际收益很低，
//	                    而每一条都要重新计费成输入 token。
//	retryAttempts       2   —— 只补一次。重试是 at-least-once，次数越多重复计费
//	                    的期望越高，而真正值得重试的只有网络抖动与 5xx。
const (
	aiDefaultRequestTimeout     = 60 * time.Second
	aiDefaultStreamTotalTimeout = 5 * time.Minute
	aiDefaultHeartbeatInterval  = 15 * time.Second
	aiDefaultHistoryMessages    = 20
	aiDefaultRetryAttempts      = 2
	aiDefaultBreakerFailures    = 5
	aiDefaultBreakerOpenTimeout = 60 * time.Second
)

// 配置取值的上下界。越界通常是手误，而手误的方向多半是"以为写了个保护，
// 实际等于没有"：把 history_messages 写成 2000 等于每轮都把整个会话重新计费一遍。
const (
	aiMinRequestTimeout     = time.Second
	aiMaxRequestTimeout     = 10 * time.Minute
	aiMaxStreamTotalTimeout = 30 * time.Minute
	aiMinHeartbeatInterval  = time.Second
	aiMaxHistoryMessages    = 200
	aiMaxTokensCeiling      = 200000
	aiMaxRetryAttempts      = 5
	aiMaxTemperature        = 2
	aiMaxBreakerOpenTimeout = 10 * time.Minute
)

// Enabled 报告 ai 模块是否装配：至少有一家厂商填了密钥。
func (c AIModuleConfig) Enabled() bool {
	return len(c.EnabledProviders()) > 0
}

// EnabledProviders 返回已启用的厂商名，按字典序。
//
// 有序是为了让"默认厂商是哪一家""启动日志里列出了谁"在两次启动之间可复现——
// map 迭代序随机，不排序的话这两处每次都不一样。
func (c AIModuleConfig) EnabledProviders() []string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(c.Providers)) {
		if c.Providers[name].Enabled() {
			names = append(names, name)
		}
	}
	return names
}

// EffectiveRequestTimeout 返回生效的非流式请求预算。
func (c AIModuleConfig) EffectiveRequestTimeout() time.Duration {
	if c.RequestTimeout > 0 {
		return c.RequestTimeout
	}
	return aiDefaultRequestTimeout
}

// EffectiveStreamTotalTimeout 返回生效的流式整体预算。
func (c AIModuleConfig) EffectiveStreamTotalTimeout() time.Duration {
	if c.StreamTotalTimeout > 0 {
		return c.StreamTotalTimeout
	}
	return aiDefaultStreamTotalTimeout
}

// EffectiveHeartbeatInterval 返回生效的 SSE 心跳间隔。
func (c AIModuleConfig) EffectiveHeartbeatInterval() time.Duration {
	if c.HeartbeatInterval > 0 {
		return c.HeartbeatInterval
	}
	return aiDefaultHeartbeatInterval
}

// EffectiveHistoryMessages 返回生效的历史消息条数。
//
// 负数按默认处理而不是按 0：把它写成 -1 多半是想表达"不限制"，
// 而"不限制"在这里没有安全的实现，退回默认比静默变成"不带历史"更接近本意。
func (c AIModuleConfig) EffectiveHistoryMessages() int {
	if c.HistoryMessages > 0 {
		return c.HistoryMessages
	}
	if c.HistoryMessages == 0 {
		return 0
	}
	return aiDefaultHistoryMessages
}

// EffectiveRetryAttempts 返回生效的总尝试次数（至少 1）。
func (c AIModuleConfig) EffectiveRetryAttempts() int {
	if c.RetryAttempts > 0 {
		return c.RetryAttempts
	}
	return aiDefaultRetryAttempts
}

// EffectiveBreakerFailures 返回生效的连续失败阈值。
func (c AIModuleConfig) EffectiveBreakerFailures() int {
	if c.Breaker.ConsecutiveFailures > 0 {
		return c.Breaker.ConsecutiveFailures
	}
	return aiDefaultBreakerFailures
}

// EffectiveBreakerOpenTimeout 返回生效的熔断开启时长。
func (c AIModuleConfig) EffectiveBreakerOpenTimeout() time.Duration {
	if c.Breaker.OpenTimeout > 0 {
		return c.Breaker.OpenTimeout
	}
	return aiDefaultBreakerOpenTimeout
}

// Validate 校验 ai 模块配置自洽。
//
// 只校验**结构与取值**，不校验厂商名是否被 LLM 库认识——后者由装配层判定
// （见 Providers 字段的说明）。
func (c AIModuleConfig) Validate() []error {
	var errs []error

	errs = append(errs, c.validateProviders()...)
	errs = append(errs, c.validateBudgets()...)
	errs = append(errs, c.validateSampling()...)

	if c.Breaker.ConsecutiveFailures < 0 {
		errs = append(errs, errors.New("modules.ai.breaker.consecutive_failures must be >= 0"))
	}
	if c.Breaker.OpenTimeout < 0 {
		errs = append(errs, errors.New("modules.ai.breaker.open_timeout must be >= 0"))
	} else if c.Breaker.OpenTimeout > aiMaxBreakerOpenTimeout {
		errs = append(errs, fmt.Errorf(
			"modules.ai.breaker.open_timeout %s exceeds %s: an open state that long keeps a "+
				"recovered provider shut out, while a half-open probe is nearly free",
			c.Breaker.OpenTimeout, aiMaxBreakerOpenTimeout))
	}

	return errs
}

// validateProviders 校验厂商表本身与默认厂商的引用。
func (c AIModuleConfig) validateProviders() []error {
	var errs []error

	for _, name := range slices.Sorted(maps.Keys(c.Providers)) {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, errors.New("modules.ai.providers has an entry with an empty name"))
			continue
		}
		if name != strings.ToLower(name) {
			errs = append(errs, fmt.Errorf(
				"modules.ai.providers key %q must be lower-case: the name is matched against the "+
					"provider library's identifiers, and a case mismatch fails at assembly time "+
					"with a message about an unknown provider", name))
		}
	}

	enabled := c.EnabledProviders()
	if len(enabled) == 0 {
		// 一家都没启用是**合法**状态：随仓模板不含真实密钥，而模板必须能通过校验。
		// 此时整个模块不装配，启动期由装配层打一条 Warn 说明端点为何不存在。
		return errs
	}

	if strings.TrimSpace(c.DefaultProvider) == "" {
		errs = append(errs, fmt.Errorf(
			"modules.ai.default_provider is required once any provider is enabled "+
				"(enabled: %v): without it every request that omits an explicit provider fails, "+
				"while the configuration looks complete", enabled))
		return errs
	}
	if !slices.Contains(enabled, c.DefaultProvider) {
		errs = append(errs, fmt.Errorf(
			"modules.ai.default_provider %q is not among the enabled providers %v: an entry is "+
				"enabled by a non-empty api_key, so this usually means the key was left blank "+
				"(or is injected under a different APP_MODULES__AI__PROVIDERS__* variable)",
			c.DefaultProvider, enabled))
	}

	return errs
}

// validateBudgets 校验三个时长旋钮及其相互关系。
func (c AIModuleConfig) validateBudgets() []error {
	var errs []error

	switch {
	case c.RequestTimeout < 0:
		errs = append(errs, errors.New("modules.ai.request_timeout must be >= 0 (0 uses the built-in default)"))
	case c.RequestTimeout > 0 && c.RequestTimeout < aiMinRequestTimeout:
		errs = append(errs, fmt.Errorf(
			"modules.ai.request_timeout %s is below the %s minimum: no model answers that fast, "+
				"so every request would time out", c.RequestTimeout, aiMinRequestTimeout))
	case c.RequestTimeout > aiMaxRequestTimeout:
		errs = append(errs, fmt.Errorf(
			"modules.ai.request_timeout %s exceeds %s: a synchronous caller has long given up by then",
			c.RequestTimeout, aiMaxRequestTimeout))
	}

	switch {
	case c.StreamTotalTimeout < 0:
		errs = append(errs, errors.New("modules.ai.stream_total_timeout must be >= 0 (0 uses the built-in default)"))
	case c.StreamTotalTimeout > aiMaxStreamTotalTimeout:
		errs = append(errs, fmt.Errorf(
			"modules.ai.stream_total_timeout %s exceeds %s: a stream that long holds a goroutine "+
				"and a connection for the whole time", c.StreamTotalTimeout, aiMaxStreamTotalTimeout))
	}

	switch {
	case c.HeartbeatInterval < 0:
		errs = append(errs, errors.New("modules.ai.heartbeat_interval must be >= 0 (0 uses the built-in default)"))
	case c.HeartbeatInterval > 0 && c.HeartbeatInterval < aiMinHeartbeatInterval:
		errs = append(errs, fmt.Errorf(
			"modules.ai.heartbeat_interval %s is below the %s minimum: a tighter loop only adds "+
				"frames without making the connection any less idle",
			c.HeartbeatInterval, aiMinHeartbeatInterval))
	}

	// 心跳必须能在流的生命周期内至少发出一次，否则它对"代理按空闲断连"毫无作用，
	// 而配置看起来是配了保活的。
	if c.EffectiveHeartbeatInterval() >= c.EffectiveStreamTotalTimeout() {
		errs = append(errs, fmt.Errorf(
			"modules.ai.heartbeat_interval (%s) must be smaller than stream_total_timeout (%s): "+
				"otherwise no keep-alive frame is ever emitted and an idle proxy still drops the "+
				"stream, while the configuration reads as if keep-alive were on",
			c.EffectiveHeartbeatInterval(), c.EffectiveStreamTotalTimeout()))
	}

	return errs
}

// validateSampling 校验上下文与采样参数。
func (c AIModuleConfig) validateSampling() []error {
	var errs []error

	if c.HistoryMessages > aiMaxHistoryMessages {
		errs = append(errs, fmt.Errorf(
			"modules.ai.history_messages %d exceeds %d: every replayed message is billed as input "+
				"tokens again on each turn, so a window that wide multiplies the cost of a long "+
				"conversation without a matching gain in answer quality",
			c.HistoryMessages, aiMaxHistoryMessages))
	}

	if c.MaxTokens < 0 {
		errs = append(errs, errors.New(
			"modules.ai.max_tokens must be >= 0 (0 omits the parameter and lets the provider decide)"))
	} else if c.MaxTokens > aiMaxTokensCeiling {
		errs = append(errs, fmt.Errorf(
			"modules.ai.max_tokens %d exceeds %d: no current model accepts an output budget that "+
				"large, and the provider rejects the whole request", c.MaxTokens, aiMaxTokensCeiling))
	}

	if c.Temperature != nil && (*c.Temperature < 0 || *c.Temperature > aiMaxTemperature) {
		errs = append(errs, fmt.Errorf(
			"modules.ai.temperature %v must be within [0, %d]; leave it unset to use the "+
				"provider's own default", *c.Temperature, aiMaxTemperature))
	}

	if c.RetryAttempts < 0 {
		errs = append(errs, errors.New(
			"modules.ai.retry_attempts must be >= 0 (0 uses the built-in default, 1 disables retrying)"))
	} else if c.RetryAttempts > aiMaxRetryAttempts {
		errs = append(errs, fmt.Errorf(
			"modules.ai.retry_attempts %d exceeds %d: retrying a chat call is at-least-once "+
				"(there is no idempotency key), so each extra attempt raises the odds of paying "+
				"twice for one answer", c.RetryAttempts, aiMaxRetryAttempts))
	}

	return errs
}

// AIWarnings 返回 ai 模块的启动期提示。
//
// 只提示不阻断：一家厂商都没配是合法的中间状态（例如先部署再灌密钥），
// 但它的表现是"AI 端点整个不存在"，而那与"路由写错了"从外部看一模一样。
// 说出来才不至于让人去查路由。
func (c *Config) AIWarnings() []string {
	if c == nil || c.Modules.AI.Enabled() {
		return nil
	}
	return []string{
		"modules.ai has no provider with an api_key: the AI chat endpoints are not registered " +
			"at all (rather than registered and always failing). Set api_key for at least one " +
			"provider, or inject it via APP_MODULES__AI__PROVIDERS__<NAME>__API_KEY",
	}
}
