package news

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gtkit/logger/v2"
)

// alertSendTimeout 是单次告警发送的独立超时：告警发生在日志调用路径上，
// 通道故障不能把日志调用方拖住。
const alertSendTimeout = 10 * time.Second

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

// ReportDelivery emits ordinary errors without SDK error text or personal data.
func ReportDelivery(delivery Delivery) {
	if delivery.Result == "success" || delivery.Result == "disabled" {
		return
	}
	logger.Errorf("alert %s send failed: result=%s duration_ms=%d",
		delivery.Channel, delivery.Result, delivery.Duration.Milliseconds())
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
	defer func() {
		if recover() != nil {
			return
		}
	}()
	if n.report != nil {
		n.report(delivery)
	}
}
