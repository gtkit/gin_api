// Package robot owns the optional default robot notification channel.
package robot

import (
	"context"
	"errors"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/notification"
)

// Opener creates a sender and optionally transfers its cleanup to this owner.
type Opener func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error)

type Options struct {
	Config *config.Config
	Open   Opener
}

// Owner exposes only protected sends; it owns any cleanup returned by Open.
type Owner struct {
	options Options
	channel notification.NotificationChannel[news.WebhookSender]
}

var _ notification.RobotProvider = (*Owner)(nil)
var _ news.WebhookSender = (*Owner)(nil)

// Open constructs the channel and opens admission without sending a notification.
// A non-nil owner must be Closed even when Open returns an error.
func Open(ctx context.Context, options Options) (*Owner, error) {
	if options.Open == nil {
		options.Open = openSender
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
	return p.channel.Init(ctx, func(ctx context.Context) (notification.NotificationClient[news.WebhookSender], bool, error) {
		var result notification.NotificationClient[news.WebhookSender]
		cfg := p.options.Config
		if cfg == nil {
			return result, false, errors.New("robot configuration is required")
		}
		var err error
		if err := ctx.Err(); err != nil {
			return result, false, err
		}
		result.Sender, result.Cleanup, err = p.options.Open(ctx, cfg.News)
		// A selected robot remains capable of explicit targets with no default URL.
		return result, true, err
	})
}

func (p *Owner) Close(ctx context.Context) error { return p.channel.Stop(ctx) }

func (p *Owner) Robot() news.WebhookSender {
	if !p.channel.Configured() {
		return nil
	}
	return p
}

func (p *Owner) Send(ctx context.Context, target, text string) error {
	return p.channel.Use(ctx, func(ctx context.Context, sender news.WebhookSender) error {
		return sender.Send(ctx, target, text)
	})
}
