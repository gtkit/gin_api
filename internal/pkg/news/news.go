package news

import (
	"context"

	"github.com/gtkit/logger/v2"
)

type Notifier struct {
	defaultURL string
	envName    string
	now        func() string
	webhook    WebhookSender
	report     func(Delivery)

	// 告警多通道：字段为 nil 即该通道未启用，Warn 扇出时跳过。
	mail        MailSender
	mailTo      []string
	sms         SMSSender
	smsPhones   []string
	smsTemplate string
}

var _ WebhookSender = (*Notifier)(nil)
var _ logger.Messager = (*Notifier)(nil)

// Send sends an alert through the configured channels using the default target.
func (n *Notifier) Send(text string) { n.SendTo("", text) }

// SendTo overrides only this alert's webhook target.
func (n *Notifier) SendTo(target, text string) { n.Warn(target, text) }

// SendContext returns channel failures and preserves the caller's cancellation.
func (n *Notifier) SendContext(ctx context.Context, target, text string) error {
	return n.WarnContext(ctx, target, text)
}

// Warn 扇出一条告警到全部已启用通道。
func (n *Notifier) Warn(url string, args ...string) {
	// Per-channel failures have already been reported by WarnContext.
	if err := n.WarnContext(context.Background(), url, args...); err != nil {
		return // Each failed channel has already emitted a non-alert diagnostic.
	}
}

func (n *Notifier) ErrRecord(msg string) {
	if n == nil {
		return
	}

	logger.Error(msg)
	n.Warn("", msg)
}

// WarnContext synchronously fans out with a separate ten-second budget per call.
// Errors remain inspectable by callers; diagnostics omit SDK errors and targets.
func (n *Notifier) WarnContext(ctx context.Context, target string, args ...string) error {
	if n == nil {
		return nil
	}
	var err error
	err = n.sendWebhook(ctx, err, target, args...)
	err = n.sendMail(ctx, err, args...)
	err = n.sendSMS(ctx, err)
	return err
}
