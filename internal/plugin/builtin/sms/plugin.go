// Package sms owns a logical-template SMS sender and its optional cleanup.
package sms

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/news"
	smsclient "github.com/gtkit/gin-api/internal/pkg/sms"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
)

type Opener func(context.Context, config.SMSConfig) (news.SMSSender, func(context.Context) error, error)

type Options struct {
	Configuration plugin.ConfigurationProvider
	Open          Opener
}

type Plugin struct {
	options Options
	channel plugin.NotificationChannel[news.SMSSender]
}

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.SMSProvider = (*Plugin)(nil)
var _ news.SMSSender = (*Plugin)(nil)

func New(options Options) *Plugin {
	if options.Open == nil {
		options.Open = openSender
	}
	return &Plugin{options: options}
}

func (*Plugin) Name() string { return "notification.sms" }

func (p *Plugin) Init(ctx context.Context) error {
	return p.channel.Init(ctx, func(ctx context.Context) (plugin.NotificationClient[news.SMSSender], bool, error) {
		var result plugin.NotificationClient[news.SMSSender]
		if err := news.ValidateSender(p.options.Configuration); err != nil {
			return result, false, fmt.Errorf("sms configuration: %w", err)
		}
		cfg, err := p.options.Configuration.Value()
		if err != nil {
			return result, false, fmt.Errorf("sms configuration: %w", err)
		}
		if cfg == nil {
			return result, false, plugin.ErrConfigurationUnavailable
		}
		if err := ctx.Err(); err != nil {
			return result, false, err
		}
		if !cfg.SMS.Enabled() {
			return result, false, nil
		}
		if cfg.SMS.Provider != "aliyun" {
			return result, false, fmt.Errorf("sms: unsupported provider")
		}
		snapshot := cfg.SMS
		snapshot.AlertPhones = slices.Clone(snapshot.AlertPhones)
		snapshot.Templates = maps.Clone(snapshot.Templates)
		result.Sender, result.Cleanup, err = p.options.Open(ctx, snapshot)
		return result, true, err
	})
}

func (p *Plugin) Start(ctx context.Context) error { return p.channel.Start(ctx) }
func (p *Plugin) Stop(ctx context.Context) error  { return p.channel.Stop(ctx) }

func (p *Plugin) SMS() news.SMSSender {
	if !p.channel.Configured() {
		return nil
	}
	return p
}

func (p *Plugin) Send(ctx context.Context, phone, template string, params map[string]string) error {
	params = maps.Clone(params)
	return p.channel.Use(ctx, func(ctx context.Context, sender news.SMSSender) error {
		return sender.Send(ctx, phone, template, params)
	})
}

func openSender(_ context.Context, cfg config.SMSConfig) (news.SMSSender, func(context.Context) error, error) {
	client, err := smsclient.NewWithConfig(cfg)
	if client == nil {
		return nil, nil, err
	}
	return client, nil, err
}
