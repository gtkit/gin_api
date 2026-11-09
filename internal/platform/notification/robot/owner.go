// Package robot owns the optional default robot notification channel.
package robot

import (
	"context"
	"errors"
	"strings"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/notification"
	"github.com/gtkit/logger/v2"
)

// Opener creates a sender and optionally transfers its cleanup to this owner.
type Opener func(context.Context, config.NewsConfig) (news.WebhookSender, func(context.Context) error, error)

type Options struct {
	Config *config.Config
	Open   Opener
}

// Owner exposes only protected sends; it owns any cleanup returned by Open.
type Owner struct {
	options    Options
	channel    notification.NotificationChannel[news.WebhookSender]
	defaultURL string
}

var _ notification.RobotProvider = (*Owner)(nil)
var _ news.WebhookSender = (*Owner)(nil)
var _ logger.Messager = (*Owner)(nil)

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
		p.defaultURL = cfg.News.FSURL
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

// Send uses the configured default target.
func (p *Owner) Send(text string) { p.SendTo("", text) }

// SendTo overrides the target for one message, without changing configuration.
func (p *Owner) SendTo(target, text string) {
	if err := p.SendContext(context.Background(), target, text); err != nil && !errors.Is(err, news.ErrSenderDisabled) {
		logger.Error("webhook notification send failed")
	}
}

// SendContext preserves caller cancellation and protects the borrowed sender.
func (p *Owner) SendContext(ctx context.Context, target, text string) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	return p.channel.Use(ctx, func(ctx context.Context, sender news.WebhookSender) error {
		if target == "" {
			target = p.defaultURL
		}
		if strings.TrimSpace(target) == "" {
			return news.ErrSenderDisabled
		}
		return sender.SendContext(ctx, target, text)
	})
}
