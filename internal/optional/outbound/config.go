package outbound

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config 是出站 HTTP 客户端的进程级配置。
//
// Clients 为空即不启用（不构造任何客户端）。母版默认不预设任何下游——
// 下游集合是每个项目自己的事实，预置一个假的只会被照抄。
type Config struct {
	// Defaults 是各客户端未显式设置时继承的默认值。
	Defaults ClientConfig `mapstructure:"defaults"`
	// Clients 按名字声明下游。名字即故障域，与熔断器的下游名一一对应。
	Clients map[string]ClientConfig `mapstructure:"clients"`
	// Breaker 是全部下游共用的熔断阈值；实例按下游名各自独立。
	//
	// 阈值共用而实例独立：一个下游的故障只熔断它自己，而"多少次连续失败
	// 算故障"是进程级的运维口径，按下游拆散只会让每个新下游多一组
	// 没人知道该填什么的配置项。零值字段取熔断包的保守默认。
	Breaker BreakerConfig `mapstructure:"breaker"`
}

// BreakerConfig 是出站熔断的阈值（outbound-http-resilience spec）。
type BreakerConfig struct {
	// ConsecutiveFailures 连续失败达到该值即熔断；0 取默认（5）。
	ConsecutiveFailures int `mapstructure:"consecutive_failures"`
	// OpenTimeout 是熔断后到半开探测的等待；0 取默认（60s）。
	OpenTimeout time.Duration `mapstructure:"open_timeout"`
	// HalfOpenMaxRequests 是半开期放行的探测请求数；0 取默认（1）。
	HalfOpenMaxRequests int `mapstructure:"half_open_max_requests"`
}

// ClientConfig 是单个出站客户端的参数。
//
// 零值字段继承 Defaults；Defaults 自身的零值由 outboundDefault* 兜底。
type ClientConfig struct {
	// BaseURL 是该下游的基地址，相对路径基于它解析。
	BaseURL string `mapstructure:"base_url"`
	// Timeout 是**单次请求的总超时**（含连接、写、读、重定向）。
	Timeout time.Duration `mapstructure:"timeout"`
	// MaxResponseBytes 是响应体读取上限，超出即报错而不是继续读。
	MaxResponseBytes int64 `mapstructure:"max_response_bytes"`
	// MaxConnsPerHost 是每主机的连接上限（含活跃与空闲）。
	MaxConnsPerHost int `mapstructure:"max_conns_per_host"`
	// MaxIdleConnsPerHost 是每主机保留的空闲连接数。
	MaxIdleConnsPerHost int `mapstructure:"max_idle_conns_per_host"`
}

// 未配置时的兜底值。
//
// 超时取 10s：出站调用没有总超时，一个卡住的下游会把本进程的 goroutine
// 与连接一起扣住，最终表现为自己被拖垮而不是下游不可用。
//
// 响应体上限取 8 MiB：一次异常的巨大响应（下游返回了 HTML 错误页、
// 或被中间设备劫持）会被整个读进内存，上限把它变成一个明确的错误。
const (
	outboundDefaultTimeout             = 10 * time.Second
	outboundDefaultMaxResponseBytes    = 8 << 20
	outboundDefaultMaxConnsPerHost     = 64
	outboundDefaultMaxIdleConnsPerHost = 16
)

// maxOutboundTimeout 是允许配置的单次请求总超时上限。
//
// 超过它的"超时"实际等于没有超时：调用方（HTTP handler）自己的处理预算
// 远小于这个量级，真等到它触发时请求早已被上游放弃。
const maxOutboundTimeout = 5 * time.Minute

// maxBreakerOpenTimeout 是熔断开启态时长的配置上限。
//
// 超过它的开启态等于把一个早已恢复的下游继续拒之门外：半开探测本身
// 几乎免费，压低探测频率没有任何收益。
const maxBreakerOpenTimeout = 10 * time.Minute

// Resolved 返回按 Defaults 与内置兜底补齐后的配置。
//
// 补齐在读取端做而不是在解码后就地改写：就地改写会让"配置文件里写了什么"
// 与"实际生效什么"在同一个结构体上分不开，排查时无从判断某个值是人写的
// 还是被补上的。
func (c ClientConfig) Resolved(defaults ClientConfig) ClientConfig {
	resolved := c
	if resolved.BaseURL == "" {
		resolved.BaseURL = defaults.BaseURL
	}
	resolved.Timeout = firstPositiveDuration(resolved.Timeout, defaults.Timeout, outboundDefaultTimeout)
	resolved.MaxResponseBytes = firstPositiveInt64(
		resolved.MaxResponseBytes, defaults.MaxResponseBytes, outboundDefaultMaxResponseBytes)
	resolved.MaxConnsPerHost = firstPositiveInt(
		resolved.MaxConnsPerHost, defaults.MaxConnsPerHost, outboundDefaultMaxConnsPerHost)
	resolved.MaxIdleConnsPerHost = firstPositiveInt(
		resolved.MaxIdleConnsPerHost, defaults.MaxIdleConnsPerHost, outboundDefaultMaxIdleConnsPerHost)
	return resolved
}

func firstPositiveDuration(values ...time.Duration) time.Duration {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstPositiveInt64(values ...int64) int64 {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstPositiveInt(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// Validate 校验出站客户端配置。
//
// 未声明任何客户端时只校验熔断阈值（写错的阈值即使暂时不生效也该被拦下，
// 否则它会在第一个下游接入时才爆）；客户端配置本身不校验：母版默认不启用
// 出站能力。
func (cfg Config) Validate() error {
	var errs []error

	if cfg.Defaults.Timeout < 0 || cfg.Defaults.MaxResponseBytes < 0 ||
		cfg.Defaults.MaxConnsPerHost < 0 || cfg.Defaults.MaxIdleConnsPerHost < 0 {
		errs = append(errs, errors.New(
			"outbound.defaults: negative values are not allowed (they silently fall back to built-ins)"))
	}
	if cfg.Breaker.ConsecutiveFailures < 0 {
		errs = append(errs, fmt.Errorf(
			"outbound.breaker.consecutive_failures %d must not be negative", cfg.Breaker.ConsecutiveFailures))
	}
	if cfg.Breaker.HalfOpenMaxRequests < 0 {
		errs = append(errs, fmt.Errorf(
			"outbound.breaker.half_open_max_requests %d must not be negative", cfg.Breaker.HalfOpenMaxRequests))
	}
	if cfg.Breaker.OpenTimeout < 0 {
		errs = append(errs, fmt.Errorf(
			"outbound.breaker.open_timeout %s must not be negative", cfg.Breaker.OpenTimeout))
	}
	if cfg.Breaker.OpenTimeout > maxBreakerOpenTimeout {
		errs = append(errs, fmt.Errorf(
			"outbound.breaker.open_timeout %s exceeds the %s cap: keeping a recovered downstream "+
				"locked out that long has no upside, half-open probes are nearly free",
			cfg.Breaker.OpenTimeout, maxBreakerOpenTimeout))
	}

	if len(cfg.Clients) == 0 {
		return errors.Join(errs...)
	}
	for name, client := range cfg.Clients {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, errors.New("outbound.clients: client name must not be empty"))
			continue
		}

		resolved := client.Resolved(cfg.Defaults)

		// base_url 必填且必须是**绝对 http(s) URL**：相对路径或缺 scheme 的值
		// 会让每次调用拼出一个本地地址，表现为莫名其妙的连接失败，而配置
		// 看起来"填了"。
		if raw := strings.TrimSpace(resolved.BaseURL); raw == "" {
			errs = append(errs, fmt.Errorf(
				"outbound.clients[%s].base_url is required: without it the downstream address "+
					"lives only in call sites and cannot be changed per environment", name))
		} else if u, parseErr := url.Parse(raw); parseErr != nil || !u.IsAbs() ||
			u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, fmt.Errorf(
				"outbound.clients[%s].base_url %q is not an absolute http(s) URL", name, resolved.BaseURL))
		}
		// 负值一律拒绝，不静默回退：回退让"配了一个非法值"与"没配"变成同一种
		// 结果，而使用者以为自己配的那个值正在生效。
		if client.Timeout < 0 || client.MaxResponseBytes < 0 ||
			client.MaxConnsPerHost < 0 || client.MaxIdleConnsPerHost < 0 {
			errs = append(errs, fmt.Errorf(
				"outbound.clients[%s]: negative values are not allowed (they silently fall back "+
					"to defaults, so the configured value never takes effect)", name))
		}
		if resolved.Timeout > maxOutboundTimeout {
			errs = append(errs, fmt.Errorf(
				"outbound.clients[%s].timeout %s exceeds the %s cap: a timeout that long is "+
					"effectively no timeout, since the inbound request budget expires far sooner",
				name, resolved.Timeout, maxOutboundTimeout))
		}
		if resolved.MaxIdleConnsPerHost > resolved.MaxConnsPerHost {
			errs = append(errs, fmt.Errorf(
				"outbound.clients[%s]: max_idle_conns_per_host (%d) exceeds max_conns_per_host (%d): "+
					"idle connections are drawn from the same budget, so the excess can never exist",
				name, resolved.MaxIdleConnsPerHost, resolved.MaxConnsPerHost))
		}
	}

	return errors.Join(errs...)
}
