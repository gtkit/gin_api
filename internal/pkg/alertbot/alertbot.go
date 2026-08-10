// Package alertbot 把群机器人（飞书 / 钉钉 / 企业微信）接成告警通道。
//
// 它实现日志库的 Messager 契约（Send / SendTo），因此 logger 的 H 系列告警
// 会原样送到群里，与既有的 news 通道并存——两者都启用时告警同时发往两处。
//
// 消息组装、加签、平台差异全部由 gtkit/msgbot 承担，本包只做三件事：
// 按配置选平台、把 Messager 的两个方法映射到它的发送接口、以及**保证告警
// 通道自身的失败不会把进程带下去**。
package alertbot

import (
	"context"
	"fmt"
	"strings"

	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gtkit/msgbot"
	"github.com/gtkit/msgbot/dingtalk"
	"github.com/gtkit/msgbot/feishu"
	"github.com/gtkit/msgbot/wecom"
)

// Notifier 是群机器人告警通道。
//
// 并发安全：msgbot 的 provider 构造后不可变，发送方法可并发调用。
type Notifier struct {
	provider msgbot.Provider
	cfg      config.AlertBotConfig
}

// New 按配置构造通道；未启用时返回 nil（调用方据此决定是否接线）。
//
// 返回 nil 而不是一个"什么都不做的通道"：告警链路上的空壳会让人以为
// 告警已接通。是否接线由装配层显式判断，判断点只有一处。
func New(cfg config.AlertBotConfig) (*Notifier, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	botCfg := msgbot.Config{
		WebhookURL: cfg.WebhookURL,
		Secret:     cfg.Secret,
		Timeout:    cfg.EffectiveTimeout(),
		Retry: msgbot.RetryPolicy{
			// msgbot 的 MaxRetries 是**首次之后**的额外次数，而配置里
			// max_attempts 是含首次的总次数——差 1，映射时必须换算，
			// 否则配 2 会变成实际尝试 3 次。
			MaxRetries: cfg.EffectiveMaxAttempts() - 1,
		},
	}

	var (
		provider msgbot.Provider
		err      error
	)
	switch strings.TrimSpace(cfg.Platform) {
	case config.AlertBotFeishu:
		provider, err = feishu.New(botCfg)
	case config.AlertBotDingTalk:
		provider, err = dingtalk.New(botCfg)
	case config.AlertBotWeCom:
		provider, err = wecom.New(botCfg)
	default:
		return nil, fmt.Errorf("alertbot: unknown platform %q", cfg.Platform)
	}
	if err != nil {
		return nil, fmt.Errorf("alertbot: build %s provider: %w", cfg.Platform, err)
	}

	return &Notifier{provider: provider, cfg: cfg}, nil
}

// Send 发送一条告警。
//
// 契约来自日志库的 Messager：**它没有返回值**。因此发送失败只能被吞掉——
// 这是有意的，也是唯一可行的：告警发送发生在日志的告警队列里，把失败再
// 记一条日志会触发新的告警，一次平台故障就能变成无限递归。
//
// 代价是"告警没发出去"这件事本身无法经告警得知。这不是本包能解决的问题：
// 告警通道自己不能成为自己的监控。真正的兜底是外部探活。
func (n *Notifier) Send(msg string) {
	if n == nil {
		return
	}
	n.send(msg)
}

// SendTo 发送告警。**url 参数被忽略**，一律发往配置的 webhook。
//
// 群机器人的 webhook 与机器人实例是绑定的：换 url 就是换一个机器人，需要
// 它自己的加签密钥与重试策略。按运行期传入的 url 现建 provider，等于每条
// 告警新建一套 HTTP 客户端与连接池——那正是本仓在出站客户端上明令禁止的
// 形态，而且告警路径上的连接泄漏最难被发现（它只在故障时才被走到）。
//
// 需要多个目标时，正确做法是装配期构造多个通道，而不是运行期拼 url。
func (n *Notifier) SendTo(_ string, msg string) {
	n.Send(msg)
}

// send 执行一次发送。
//
// 用独立 ctx 而不是背景无限等待：provider 自带超时，但显式带上预算能保证
// 即使平台侧连接挂起，这次调用也一定会返回——它占的是告警队列的处理槽位。
func (n *Notifier) send(msg string) {
	if strings.TrimSpace(msg) == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), n.cfg.EffectiveTimeout())
	defer cancel()

	_ = n.provider.SendText(ctx, msg)
}

// Platform 返回通道所用的平台标识，供装配期日志确认接的是哪一家。
func (n *Notifier) Platform() string {
	if n == nil {
		return ""
	}
	return string(n.provider.Platform())
}
