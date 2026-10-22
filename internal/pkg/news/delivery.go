package news

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/stringx"
)

var (
	ErrSenderMissing  = errors.New("notification sender is missing")
	ErrSenderDisabled = errors.New("notification channel is disabled")
	ErrNotRunning     = errors.New("notification is not running")
	ErrStopped        = errors.New("notification is stopped")
	ErrNotDrained     = errors.New("notification resources are still in use")
	ErrSenderPanic    = errors.New("notification implementation panicked")
)

// Delivery contains only bounded diagnostic values, never targets or payloads.
type Delivery struct {
	Channel  string
	Result   string
	Duration time.Duration
}

// Options injects borrowed senders and a snapshot of alert destinations.
type Options struct {
	DefaultURL  string
	Environment string
	MailTo      []string
	SMSPhones   []string
	SMSTemplate string
	Webhook     WebhookSender
	Mail        MailSender
	SMS         SMSSender
	Report      func(Delivery)
	Now         func() time.Time
}

// ValidateSender rejects nil interfaces and typed nils at assembly boundaries.
// Reflection is confined here: arbitrary implementations can have any nilable
// concrete type. No method is invoked and no sending path uses reflection.
func ValidateSender(sender any) error {
	if sender == nil {
		return ErrSenderMissing
	}
	value := reflect.ValueOf(sender)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return ErrSenderMissing
		}
	}
	return nil
}

// New builds an injected notifier without constructing or owning any sender.
func New(options Options) (*Notifier, error) {
	for _, sender := range []any{options.Webhook, options.Mail, options.SMS} {
		if sender != nil {
			if err := ValidateSender(sender); err != nil {
				return nil, err
			}
		}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	report := options.Report
	if report == nil {
		report = ReportDelivery
	}
	n := &Notifier{
		defaultURL:  options.DefaultURL,
		envName:     options.Environment,
		now:         func() string { return now().Format(time.DateTime) },
		report:      report,
		mail:        options.Mail,
		mailTo:      slices.Clone(options.MailTo),
		sms:         options.SMS,
		smsPhones:   slices.Clone(options.SMSPhones),
		smsTemplate: options.SMSTemplate,
	}
	if options.Webhook != nil {
		n.webhookSender = func(ctx context.Context, target string, args ...string) error {
			return options.Webhook.Send(ctx, target, strings.Join(args, " "))
		}
	}
	return n, nil
}

// ReportDelivery emits ordinary errors without SDK error text or personal data.
func ReportDelivery(delivery Delivery) {
	if delivery.Result == "success" || delivery.Result == "disabled" {
		return
	}
	applog.Errorf("alert %s send failed: result=%s duration_ms=%d",
		delivery.Channel, delivery.Result, delivery.Duration.Milliseconds())
}

// WarnContext synchronously fans out with a separate ten-second budget per call.
// Errors remain inspectable by callers; diagnostics omit SDK errors and targets.
func (n *Notifier) WarnContext(ctx context.Context, target string, args ...string) error {
	if n == nil {
		return nil
	}
	if target == "" {
		target = n.defaultURL
	}
	var result error
	if strings.TrimSpace(target) == "" {
		n.emit(Delivery{Channel: "webhook", Result: "disabled"})
	} else {
		title := stringx.BuilderJoin([]string{n.envName, " time: ", n.now()})
		callArgs := append([]string{title}, args...)
		result = errors.Join(result, n.deliver(ctx, "webhook", func(callCtx context.Context) error {
			if n.webhookSender == nil {
				return ErrSenderMissing
			}
			return n.webhookSender(callCtx, target, callArgs...)
		}))
	}
	if n.mail != nil && len(n.mailTo) > 0 {
		result = errors.Join(result, n.deliver(ctx, "mail", func(callCtx context.Context) error {
			return n.mail.Send(callCtx, MailMessage{
				To: slices.Clone(n.mailTo), Subject: "[" + n.envName + "] service alert", Text: strings.Join(args, " "),
			})
		}))
	} else {
		n.emit(Delivery{Channel: "mail", Result: "disabled"})
	}
	if n.sms != nil && n.smsTemplate != "" {
		for _, phone := range n.smsPhones {
			result = errors.Join(result, n.deliver(ctx, "sms", func(callCtx context.Context) error {
				return n.sms.Send(callCtx, phone, n.smsTemplate, map[string]string{"env": n.envName})
			}))
		}
	} else {
		n.emit(Delivery{Channel: "sms", Result: "disabled"})
	}
	return result
}

func (n *Notifier) deliver(parent context.Context, channel string, send func(context.Context) error) error {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, alertSendTimeout)
	defer cancel()
	err := callSender(ctx, send)
	n.emit(Delivery{Channel: channel, Result: deliveryResult(err), Duration: time.Since(started)})
	if err != nil {
		return fmt.Errorf("alert %s: %w", channel, err)
	}
	return nil
}

func callSender(ctx context.Context, send func(context.Context) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrSenderPanic
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(send(ctx), ctx.Err())
}

func deliveryResult(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrSenderPanic):
		return "panic"
	case errors.Is(err, ErrStopped), errors.Is(err, ErrNotRunning):
		return "stopped"
	case errors.Is(err, ErrSenderMissing):
		return "missing"
	case errors.Is(err, ErrSenderDisabled):
		return "disabled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "provider_error"
	}
}

func (n *Notifier) emit(delivery Delivery) {
	// A diagnostic callback is not allowed to interrupt the remaining channels.
	defer func() { _ = recover() }()
	if n.report != nil {
		n.report(delivery)
	}
}
