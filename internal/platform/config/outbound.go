package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// OutboundConfig 是出站 HTTP 客户端的进程级配置。
//
// Clients 为空即不启用（不构造任何客户端）。母版默认不预设任何下游——
// 下游集合是每个项目自己的事实，预置一个假的只会被照抄。
type OutboundConfig struct {
	// Defaults 是各客户端未显式设置时继承的默认值。
	Defaults OutboundClientConfig `mapstructure:"defaults"`
	// Clients 按名字声明下游。名字即故障域，与熔断器的下游名一一对应。
	Clients map[string]OutboundClientConfig `mapstructure:"clients"`
}

// OutboundClientConfig 是单个出站客户端的参数。
//
// 零值字段继承 Defaults；Defaults 自身的零值由 outboundDefault* 兜底。
type OutboundClientConfig struct {
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

// Resolved 返回按 Defaults 与内置兜底补齐后的配置。
//
// 补齐在读取端做而不是在解码后就地改写：就地改写会让"配置文件里写了什么"
// 与"实际生效什么"在同一个结构体上分不开，排查时无从判断某个值是人写的
// 还是被补上的。
func (c OutboundClientConfig) Resolved(defaults OutboundClientConfig) OutboundClientConfig {
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

// validateOutboundConfig 校验出站客户端配置。
//
// 未声明任何客户端时不校验：母版默认不启用出站能力。
func validateOutboundConfig(cfg OutboundConfig) error {
	if len(cfg.Clients) == 0 {
		return nil
	}

	var errs []error
	for name, client := range cfg.Clients {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, errors.New("outbound.clients: client name must not be empty"))
			continue
		}

		resolved := client.Resolved(cfg.Defaults)

		// base_url 必填：没有它，"这个名字对应哪个下游"只存在于调用点的字符串里，
		// 换环境时没有任何一处可改。
		if strings.TrimSpace(resolved.BaseURL) == "" {
			errs = append(errs, fmt.Errorf(
				"outbound.clients[%s].base_url is required: without it the downstream address "+
					"lives only in call sites and cannot be changed per environment", name))
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
