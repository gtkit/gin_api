// Package httpmiddleware implements the scaffold's default ordered Gin chain.
package httpmiddleware

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/plugin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// RequestIDFactory is the default implementation's startup-only constructor.
type RequestIDFactory func() (*middleware.RequestIDGenerator, error)

// Options contains named startup dependencies; no work happens in New.
type Options struct {
	Configuration    plugin.ConfigurationProvider
	RequestIDFactory RequestIDFactory
}

// Plugin freezes configuration at Init and builds one chain after Start.
type Plugin struct {
	options                                     Options
	mu                                          sync.Mutex
	initialized, ready, started, stopped, built bool
	initErr, buildErr                           error
	origins                                     []string
	logRequestBody, logResponseBody             bool
	maxBodyBytes                                int64
}

var _ core.Plugin = (*Plugin)(nil)
var _ plugin.MiddlewareBuilder = (*Plugin)(nil)

// New creates no handlers, random prefix or observation instruments.
func New(options Options) *Plugin {
	if options.RequestIDFactory == nil {
		options.RequestIDFactory = middleware.NewRequestIDGenerator
	}
	return &Plugin{options: options}
}
func (*Plugin) Name() string { return "http-middleware" }
func (p *Plugin) Init(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.initialized {
		return p.initErr
	}
	p.initialized = true
	p.initErr = p.initialize(ctx)
	p.ready = p.initErr == nil
	return p.initErr
}
func (p *Plugin) initialize(ctx context.Context) error {
	if p.stopped {
		return fmt.Errorf("init HTTP middleware after stop: %w", core.ErrPluginContractViolation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.options.Configuration == nil {
		return errors.New("HTTP middleware configuration provider is required")
	}
	cfg, err := p.options.Configuration.Value()
	if err != nil {
		return fmt.Errorf("read HTTP middleware config: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("read HTTP middleware config: %w", core.ErrPluginContractViolation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.origins = slices.Clone(cfg.CORS.AllowedOrigins)
	p.logRequestBody = cfg.Log.AccessRequestBody
	p.logResponseBody = cfg.Log.AccessResponseBody
	p.maxBodyBytes = cfg.App.MaxBodyBytes
	return nil
}
func (p *Plugin) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ready || p.stopped {
		return fmt.Errorf("start HTTP middleware: %w: configuration unavailable", core.ErrPluginContractViolation)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.started = true
	return nil
}

// Build runs once after configuration/logging and the final metrics instruments.
func (p *Plugin) Build(deps plugin.MiddlewareDeps) ([]gin.HandlerFunc, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.stopped {
		return nil, fmt.Errorf("build HTTP middleware: %w: lifecycle not running", core.ErrPluginContractViolation)
	}
	if p.built {
		return nil, errors.Join(plugin.ErrMiddlewareAlreadyBuilt, p.buildErr)
	}
	p.built = true
	chain, err := p.build(deps)
	p.buildErr = err
	return chain, err
}
func (p *Plugin) build(deps plugin.MiddlewareDeps) ([]gin.HandlerFunc, error) {
	if deps.Metrics == nil {
		return nil, errors.New("HTTP middleware requires final metrics recorder before Build")
	}
	generator, err := p.options.RequestIDFactory()
	if err != nil {
		return nil, fmt.Errorf("build HTTP request ID generator: %w", err)
	}
	if generator == nil {
		return nil, fmt.Errorf("build HTTP request ID generator: %w: nil generator", core.ErrPluginContractViolation)
	}
	chain := make([]gin.HandlerFunc, 0, 10)
	chain = append(chain, middleware.GinRecovery(true), middleware.RequestID(generator))
	if deps.TracingEnabled {
		chain = append(chain, otelgin.Middleware(deps.ServiceName))
	}
	chain = append(chain, middleware.GinLogger(middleware.AccessLogOptions{LogRequestBody: p.logRequestBody, LogResponseBody: p.logResponseBody}), middleware.Metrics(deps.Metrics), middleware.GinRecovery(true), middleware.SecurityHeaders(), middleware.ErrorHandler())
	if len(p.origins) > 0 {
		chain = append(chain, middleware.Cors(p.origins))
	}
	chain = append(chain, middleware.MaxBytesLimit(middleware.MaxBytesLimitConfig{MaxBytes: p.maxBodyBytes}))
	return chain, nil
}

// Stop owns no borrowed resources; already returned handlers remain immutable.
func (p *Plugin) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	return nil
}
