package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// AlertBotConfig 是群机器人告警通道的配置。
//
// 默认关闭。它与既有的 news 通道并存而非替代——两者都启用时告警同时发往两处。
// 不做成替代是因为切换告警通道是运维决策：静默改变"告警发到哪里"，会让某个
// 值班群在毫无察觉的情况下不再收到消息。
type AlertBotConfig struct {
	// Enabled 是总开关。
	Enabled bool `mapstructure:"enabled"`
	// Platform 是机器人平台：feishu、dingtalk 或 wecom。
	Platform string `mapstructure:"platform"`
	// WebhookURL 是机器人的 webhook 地址。
	WebhookURL string `mapstructure:"webhook_url"`
	// Secret 是加签密钥。平台侧开启"加签"时必填。
	Secret string `mapstructure:"secret"`
	// Timeout 是单次发送的超时。
	Timeout time.Duration `mapstructure:"timeout"`
	// MaxAttempts 是发送失败后的总尝试次数（含首次）。
	MaxAttempts int `mapstructure:"max_attempts"`
}

// 支持的机器人平台。
const (
	AlertBotFeishu   = "feishu"
	AlertBotDingTalk = "dingtalk"
	AlertBotWeCom    = "wecom"
)

// 未配置时的兜底值。
//
// 超时取 5s：告警发送发生在日志的告警队列里，卡住会拖住后续告警的投递——
// 一条发不出去的告警不该让后面所有告警一起排队。
//
// 尝试次数取 2（首次 + 1 次重试）：告警重试的价值只在于跨过一次瞬时抖动，
// 次数多了只会在平台真的挂掉时把告警队列堵死。
const (
	alertBotDefaultTimeout     = 5 * time.Second
	alertBotDefaultMaxAttempts = 2
)

// maxAlertBotTimeout 是发送超时上限。
const maxAlertBotTimeout = 30 * time.Second

// EffectiveTimeout 返回生效的发送超时。
func (c AlertBotConfig) EffectiveTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return alertBotDefaultTimeout
}

// EffectiveMaxAttempts 返回生效的尝试次数。
func (c AlertBotConfig) EffectiveMaxAttempts() int {
	if c.MaxAttempts > 0 {
		return c.MaxAttempts
	}
	return alertBotDefaultMaxAttempts
}

// validateAlertBotConfig 校验群机器人配置。
func validateAlertBotConfig(cfg AlertBotConfig) error {
	if !cfg.Enabled {
		return nil
	}

	var errs []error

	switch strings.TrimSpace(cfg.Platform) {
	case AlertBotFeishu, AlertBotDingTalk, AlertBotWeCom:
	case "":
		errs = append(errs, fmt.Errorf(
			"alertbot.platform is required when the bot is enabled (%q, %q or %q)",
			AlertBotFeishu, AlertBotDingTalk, AlertBotWeCom))
	default:
		errs = append(errs, fmt.Errorf(
			"unknown alertbot.platform %q (want %q, %q or %q)",
			cfg.Platform, AlertBotFeishu, AlertBotDingTalk, AlertBotWeCom))
	}

	// webhook 地址缺失时通道构造会成功而发送永远失败——告警系统自己不告警，
	// 这种失效只能靠"怎么一直没收到告警"来发现。
	if strings.TrimSpace(cfg.WebhookURL) == "" {
		errs = append(errs, errors.New(
			"alertbot.webhook_url is required when the bot is enabled: without it every alert "+
				"fails silently, and an alerting channel cannot alert about itself"))
	}
	if cfg.EffectiveTimeout() > maxAlertBotTimeout {
		errs = append(errs, fmt.Errorf(
			"alertbot.timeout %s exceeds the %s cap: alerts are sent from the log alert queue, "+
				"and one stuck send would hold up every alert behind it",
			cfg.EffectiveTimeout(), maxAlertBotTimeout))
	}

	return errors.Join(errs...)
}
