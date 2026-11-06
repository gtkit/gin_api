package news

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// MailMessage 是项目内的纯文本邮件契约，不包含供应商 SDK 类型。
type MailMessage struct {
	To      []string
	Subject string
	Text    string
}

// MailSender 发送项目邮件，发送须遵守 ctx 的取消与期限。
// 使用方借用该能力，不取得底层客户端的关闭权限。
type MailSender interface {
	Send(ctx context.Context, message MailMessage) error
}

// WithMail borrows a mail sender and snapshots its alert recipients.
// A nil sender or no recipients disables mail alerts.
func WithMail(sender MailSender, recipients ...string) Option {
	recipients = slices.Clone(recipients)
	return func(o *options) { o.mail, o.mailTo = sender, recipients }
}

func (n *Notifier) sendMail(ctx context.Context, result error, args ...string) error {
	if n.mail != nil && len(n.mailTo) > 0 {
		result = errors.Join(result, n.deliver(ctx, "mail", func(callCtx context.Context) error {
			return n.mail.Send(callCtx, MailMessage{
				To: slices.Clone(n.mailTo), Subject: "[" + n.envName + "] service alert", Text: strings.Join(args, " "),
			})
		}))
	} else {
		n.emit(Delivery{Channel: "mail", Result: "disabled"})
	}
	return result
}
