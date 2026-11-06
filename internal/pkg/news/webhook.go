package news

import (
	"context"
	"errors"
	"strings"

	"github.com/gtkit/logger/v2"
)

// WebhookSender 可直接传给 logger.WithMessager；SendContext 保留取消和错误返回。
// Send/SendTo 使用默认/指定目标尽力发送，失败仅记录普通诊断。
// 实现须遵守 ctx 的取消与期限；使用方不取得底层客户端的关闭权限。
type WebhookSender interface {
	AlertSink
	SendContext(ctx context.Context, target, text string) error
}

// ImageMessageSender is implemented directly by msgbot/feishu.App.
type ImageMessageSender interface {
	SendImageMessage(ctx context.Context, openID, path string) error
}

// WithDefaultURL sets the webhook target; empty skips default webhook sends.
func WithDefaultURL(target string) Option {
	return func(o *options) { o.defaultURL = target }
}

// WithWebhook borrows a webhook sender; nil leaves it unconfigured.
// Sending to a nonempty target without a sender returns ErrSenderMissing.
func WithWebhook(sender WebhookSender) Option {
	return func(o *options) { o.webhook = sender }
}

func (n *Notifier) sendWebhook(ctx context.Context, result error, target string, args ...string) error {
	if target == "" {
		target = n.defaultURL
	}
	if strings.TrimSpace(target) == "" {
		n.emit(Delivery{Channel: "webhook", Result: "disabled"})
	} else {
		title := n.envName + " time: " + n.now()
		callArgs := append([]string{title}, args...)
		result = errors.Join(result, n.deliver(ctx, "webhook", func(callCtx context.Context) error {
			if n.webhook == nil {
				return ErrSenderMissing
			}
			return n.webhook.SendContext(callCtx, target, strings.Join(callArgs, " "))
		}))
	}
	return result
}

func SendPicMsg(ctx context.Context, newsApp ImageMessageSender, openids, shotname string) error {
	openidSlice := strings.SplitSeq(openids, ",")
	for openid := range openidSlice {
		openid = strings.TrimSpace(openid)
		if openid == "" {
			continue
		}
		if err := newsApp.SendImageMessage(ctx, openid, shotname); err != nil {
			logger.Errorf("send image message error:%v, openid:%s", err, openid)
		}
	}
	return nil
}
