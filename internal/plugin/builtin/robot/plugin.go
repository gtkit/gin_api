// Package robot owns the optional default robot notification channel.
package robot

import (
	"context"
	"fmt"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
)

// Opener creates a sender and optionally transfers its cleanup to this plugin.
type Opener func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error)

type Options struct {
	Configuration plugin.ConfigurationProvider
	Open          Opener
}

// Plugin exposes only protected sends; it owns any cleanup returned by Open.
type Plugin struct {
	options Options
	channel plugin.NotificationChannel[news.WebhookSender]
}

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.RobotProvider = (*Plugin)(nil)
var _ news.WebhookSender = (*Plugin)(nil)

// New stores configuration and factory references without calling either.
func New(options Options) *Plugin {
	if options.Open == nil {
		options.Open = openSender
	}
	return &Plugin{options: options}
}

func (*Plugin) Name() string { return "notification.robot" }

func (p *Plugin) Init(ctx context.Context) error {
	return p.channel.Init(ctx, func(ctx context.Context) (plugin.NotificationClient[news.WebhookSender], bool, error) {
		var result plugin.NotificationClient[news.WebhookSender]
		if err := news.ValidateSender(p.options.Configuration); err != nil {
			return result, false, fmt.Errorf("robot configuration: %w", err)
		}
		cfg, err := p.options.Configuration.Value()
		if err != nil {
			return result, false, fmt.Errorf("robot configuration: %w", err)
		}
		if cfg == nil {
			return result, false, plugin.ErrConfigurationUnavailable
		}
		if err := ctx.Err(); err != nil {
			return result, false, err
		}
		result.Sender, result.Cleanup, err = p.options.Open(ctx, cfg.News)
		// A selected robot remains capable of explicit targets with no default URL.
		return result, true, err
	})
}

func (p *Plugin) Start(ctx context.Context) error { return p.channel.Start(ctx) }
func (p *Plugin) Stop(ctx context.Context) error  { return p.channel.Stop(ctx) }

func (p *Plugin) Robot() news.WebhookSender {
	if !p.channel.Configured() {
		return nil
	}
	return p
}

func (p *Plugin) Send(ctx context.Context, target, text string) error {
	return p.channel.Use(ctx, func(ctx context.Context, sender news.WebhookSender) error {
		return sender.Send(ctx, target, text)
	})
}
