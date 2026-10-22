// Package sms owns a logical-template SMS sender and its optional cleanup.
package sms

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/gtkit/gin-api/internal/pkg/news"
	smsclient "github.com/gtkit/gin-api/internal/pkg/sms"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/notification"
)

type Opener func(context.Context, config.SMSConfig) (news.SMSSender, func(context.Context) error, error)

type Options struct {
	Config *config.Config
	Open   Opener
}

type Owner struct {
	options Options
	channel notification.NotificationChannel[news.SMSSender]
}

var _ notification.SMSProvider = (*Owner)(nil)
var _ news.SMSSender = (*Owner)(nil)

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
	return p.channel.Init(ctx, func(ctx context.Context) (notification.NotificationClient[news.SMSSender], bool, error) {
		var result notification.NotificationClient[news.SMSSender]
		cfg := p.options.Config
		if cfg == nil {
			return result, false, errors.New("sms configuration is required")
		}
		var err error
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

func (p *Owner) Close(ctx context.Context) error { return p.channel.Stop(ctx) }

func (p *Owner) SMS() news.SMSSender {
	if !p.channel.Configured() {
		return nil
	}
	return p
}

func (p *Owner) Send(ctx context.Context, phone, template string, params map[string]string) error {
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
