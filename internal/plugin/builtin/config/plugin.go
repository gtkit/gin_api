package config

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gtkit/gin-api/internal/core"
	schema "github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
)

// Loader supplies startup configuration; Plugin validates it before publishing.
type Loader func(context.Context) (*schema.Config, error)

// Plugin owns one immutable startup snapshot and no background work.
type Plugin struct {
	load Loader
	once sync.Once
	mu   sync.RWMutex
	cfg  *schema.Config
	err  error
}

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.ConfigurationProvider = (*Plugin)(nil)

// New creates a configuration plugin without loading any files. A nil loader
// selects the default YAML/APP_ loader; custom loaders use the same validation.
func New(loader Loader) *Plugin {
	if loader == nil {
		loader = func(context.Context) (*schema.Config, error) { return Load() }
	}
	return &Plugin{load: loader}
}

// Name returns the registry name.
func (*Plugin) Name() string { return "config" }

// Init loads and publishes a validated snapshot once, including a cached failure.
func (p *Plugin) Init(ctx context.Context) error {
	p.once.Do(func() {
		var cfg *schema.Config
		err := ctx.Err()
		if err == nil {
			cfg, err = p.load(ctx)
		}
		if err == nil && cfg == nil {
			err = fmt.Errorf("load config: %w: loader returned nil", core.ErrPluginContractViolation)
		}
		if err == nil {
			err = cfg.Validate()
		}
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			err = errors.Join(err, ctxErr)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.err = err
		if err == nil {
			p.cfg = cfg
		}
	})
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.err
}

// Value returns the read-only validated snapshot, or a distinguishable error.
func (p *Plugin) Value() (*schema.Config, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cfg == nil {
		return nil, errors.Join(plugin.ErrConfigurationUnavailable, p.err)
	}
	return p.cfg, nil
}

// Start verifies that configuration is ready; it starts no background work.
func (p *Plugin) Start(ctx context.Context) error {
	_, err := p.Value()
	return errors.Join(ctx.Err(), err)
}

// Stop has no resources to release. Configuration remains readable for cleanup.
func (*Plugin) Stop(context.Context) error { return nil }
