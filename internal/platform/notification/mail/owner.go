// Package mail owns a mail sender while exporting only its borrowed capability.
package mail

import (
	"context"
	"errors"
	"slices"

	mailclient "github.com/gtkit/gin-api/internal/pkg/mail"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/notification"
)

type Opener func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error)

type Options struct {
	Config *config.Config
	Open   Opener
}

type Owner struct {
	options Options
	channel notification.NotificationChannel[news.MailSender]
}

var _ notification.MailProvider = (*Owner)(nil)
var _ news.MailSender = (*Owner)(nil)

// Open returns an owner even on failure; callers must Close any non-nil owner.
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
	return p.channel.Init(ctx, func(ctx context.Context) (notification.NotificationClient[news.MailSender], bool, error) {
		var result notification.NotificationClient[news.MailSender]
		cfg := p.options.Config
		if cfg == nil {
			return result, false, errors.New("mail configuration is required")
		}
		var err error
		if err := ctx.Err(); err != nil {
			return result, false, err
		}
		if !cfg.Email.Enabled() {
			return result, false, nil
		}
		snapshot := cfg.Email
		snapshot.AlertTo = slices.Clone(snapshot.AlertTo)
		result.Sender, result.Cleanup, err = p.options.Open(ctx, snapshot)
		return result, true, err
	})
}

func (p *Owner) Close(ctx context.Context) error { return p.channel.Stop(ctx) }

func (p *Owner) Mail() news.MailSender {
	if !p.channel.Configured() {
		return nil
	}
	return p
}

func (p *Owner) Send(ctx context.Context, message news.MailMessage) error {
	message.To = slices.Clone(message.To)
	return p.channel.Use(ctx, func(ctx context.Context, sender news.MailSender) error {
		return sender.Send(ctx, message)
	})
}

type sender struct{ client *mailclient.Client }

func (s *sender) Send(ctx context.Context, message news.MailMessage) error {
	return s.client.Send(ctx, mailclient.Message{To: message.To, Subject: message.Subject, Text: message.Text})
}

func openSender(_ context.Context, cfg config.EmailConfig) (news.MailSender, func(context.Context) error, error) {
	client, err := mailclient.NewWithConfig(cfg)
	if client == nil {
		return nil, nil, err
	}
	// The existing mail client has no Close method or owned background worker.
	return &sender{client: client}, nil, err
}
