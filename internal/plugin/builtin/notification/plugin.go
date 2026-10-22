// Package notification combines borrowed channels without owning their clients.
package notification

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/plugin"
)

type Options struct {
	Configuration plugin.ConfigurationProvider
	Robot         plugin.RobotProvider
	Mail          plugin.MailProvider
	SMS           plugin.SMSProvider
	Report        func(news.Delivery)
}

type Plugin struct {
	options Options
	channel plugin.NotificationChannel[*news.Notifier]
}

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.NotificationSink = (*Plugin)(nil)

// New stores dependencies without reading config, invoking factories or sending.
func New(options Options) *Plugin {
	if options.Report == nil {
		options.Report = news.ReportDelivery
	}
	report := options.Report
	options.Report = func(delivery news.Delivery) {
		defer func() { recover() }()
		report(delivery)
	}
	return &Plugin{options: options}
}

func (*Plugin) Name() string { return "notification" }

func (p *Plugin) Init(ctx context.Context) error {
	return p.channel.Init(ctx, func(context.Context) (plugin.NotificationClient[*news.Notifier], bool, error) {
		var result plugin.NotificationClient[*news.Notifier]
		if err := news.ValidateSender(p.options.Configuration); err != nil {
			return result, false, fmt.Errorf("notification configuration: %w", err)
		}
		cfg, err := p.options.Configuration.Value()
		if err != nil {
			return result, false, fmt.Errorf("notification configuration: %w", err)
		}
		if cfg == nil {
			return result, false, plugin.ErrConfigurationUnavailable
		}
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

func (p *Plugin) Start(ctx context.Context) error { return p.channel.Start(ctx) }
func (p *Plugin) Stop(ctx context.Context) error  { return p.channel.Stop(ctx) }

func (p *Plugin) Warn(target string, args ...string) {
	// Warn has no receipt; Notify reports each failure through the safe callback.
	if err := p.Notify(context.Background(), target, args...); err != nil {
		// Notify has already emitted the non-alert diagnostic.
		return
	}
}

// Notify returns aggregate channel errors without adding a second queue.
func (p *Plugin) Notify(ctx context.Context, target string, args ...string) error {
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
