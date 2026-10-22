// Package mail owns a mail sender while exporting only its borrowed capability.
package mail

import (
	"context"
	"fmt"
	"slices"

	"github.com/gtkit/gin-api/internal/core"
	mailclient "github.com/gtkit/gin-api/internal/pkg/mail"
	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
)

type Opener func(context.Context, config.EmailConfig) (news.MailSender, func(context.Context) error, error)

type Options struct {
	Configuration plugin.ConfigurationProvider
	Open          Opener
}

type Plugin struct {
	options Options
	channel plugin.NotificationChannel[news.MailSender]
}

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.MailProvider = (*Plugin)(nil)
var _ news.MailSender = (*Plugin)(nil)

func New(options Options) *Plugin {
	if options.Open == nil {
		options.Open = openSender
	}
	return &Plugin{options: options}
}

func (*Plugin) Name() string { return "notification.mail" }

func (p *Plugin) Init(ctx context.Context) error {
	return p.channel.Init(ctx, func(ctx context.Context) (plugin.NotificationClient[news.MailSender], bool, error) {
		var result plugin.NotificationClient[news.MailSender]
		if err := news.ValidateSender(p.options.Configuration); err != nil {
			return result, false, fmt.Errorf("mail configuration: %w", err)
		}
		cfg, err := p.options.Configuration.Value()
		if err != nil {
			return result, false, fmt.Errorf("mail configuration: %w", err)
		}
		if cfg == nil {
			return result, false, plugin.ErrConfigurationUnavailable
		}
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

func (p *Plugin) Start(ctx context.Context) error { return p.channel.Start(ctx) }
func (p *Plugin) Stop(ctx context.Context) error  { return p.channel.Stop(ctx) }

func (p *Plugin) Mail() news.MailSender {
	if !p.channel.Configured() {
		return nil
	}
	return p
}

func (p *Plugin) Send(ctx context.Context, message news.MailMessage) error {
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
