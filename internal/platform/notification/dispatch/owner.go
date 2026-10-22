// Package dispatch combines borrowed channels without owning their clients.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/notification"
)

type Options struct {
	Config *config.Config
	Robot  notification.RobotProvider
	Mail   notification.MailProvider
	SMS    notification.SMSProvider
	Report func(news.Delivery)
}

type Owner struct {
	options Options
	channel notification.NotificationChannel[*news.Notifier]
}

var _ notification.NotificationSink = (*Owner)(nil)

// Open combines already opened channels without sending a notification.
// A non-nil owner must be Closed even when Open returns an error.
func Open(ctx context.Context, options Options) (*Owner, error) {
	if options.Report == nil {
		options.Report = news.ReportDelivery
	}
	report := options.Report
	options.Report = func(delivery news.Delivery) {
		defer func() {
			if recover() != nil {
				return
			}
		}()
		report(delivery)
	}
	p := &Owner{options: options}
	if err := p.initialize(ctx); err != nil {
		return p, err
	}
	if err := p.channel.Start(ctx); err != nil {
		return p, err
	}
	return p, nil
}

func (p *Owner) initialize(ctx context.Context) error {
	return p.channel.Init(ctx, func(context.Context) (notification.NotificationClient[*news.Notifier], bool, error) {
		var result notification.NotificationClient[*news.Notifier]
		cfg := p.options.Config
		if cfg == nil {
			return result, false, errors.New("dispatch configuration is required")
		}
		var err error
		options := news.Options{DefaultURL: cfg.News.FSURL, Environment: string(cfg.Env), Report: p.options.Report}
		if p.options.Robot != nil {
			if err := news.ValidateSender(p.options.Robot); err != nil {
				return result, false, fmt.Errorf("robot provider: %w", err)
			}
			options.Webhook = p.options.Robot.Robot()
			if err := news.ValidateSender(options.Webhook); err != nil {
				return result, false, fmt.Errorf("robot channel: %w", err)
			}
		} else if cfg.News.FSURL != "" {
			return result, false, fmt.Errorf("configured robot channel: %w", news.ErrSenderMissing)
		}
		if cfg.Email.Enabled() {
			if err := news.ValidateSender(p.options.Mail); err != nil {
				return result, false, fmt.Errorf("configured mail provider: %w", err)
			}
			options.Mail = p.options.Mail.Mail()
			if err := news.ValidateSender(options.Mail); err != nil {
				return result, false, fmt.Errorf("configured mail channel: %w", err)
			}
			options.MailTo = slices.Clone(cfg.Email.AlertTo)
		}
		if cfg.SMS.Enabled() {
			if err := news.ValidateSender(p.options.SMS); err != nil {
				return result, false, fmt.Errorf("configured sms provider: %w", err)
			}
			options.SMS = p.options.SMS.SMS()
			if err := news.ValidateSender(options.SMS); err != nil {
				return result, false, fmt.Errorf("configured sms channel: %w", err)
			}
			options.SMSPhones = slices.Clone(cfg.SMS.AlertPhones)
			options.SMSTemplate = cfg.SMS.AlertTemplate
		}
		result.Sender, err = news.New(options)
		return result, true, err
	})
}

func (p *Owner) Close(ctx context.Context) error { return p.channel.Stop(ctx) }

func (p *Owner) Warn(target string, args ...string) {
	// Warn has no receipt; Notify reports each failure through the safe callback.
	if err := p.Notify(context.Background(), target, args...); err != nil {
		// Notify has already emitted the non-alert diagnostic.
		return
	}
}

// Notify returns aggregate channel errors without adding a second queue.
func (p *Owner) Notify(ctx context.Context, target string, args ...string) error {
	entered := false
	err := p.channel.Use(ctx, func(ctx context.Context, notifier *news.Notifier) error {
		entered = true
		return notifier.WarnContext(ctx, target, args...)
	})
	if err != nil && !entered {
		result := "stopped"
		if errors.Is(err, context.DeadlineExceeded) {
			result = "timeout"
		} else if errors.Is(err, context.Canceled) {
			result = "canceled"
		}
		p.options.Report(news.Delivery{Channel: "notification", Result: result})
	}
	return err
}
