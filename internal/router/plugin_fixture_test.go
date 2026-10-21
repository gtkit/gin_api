package router

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/plugin"
	defaulthttp "github.com/gtkit/gin-api/internal/plugin/builtin/httpmiddleware"
	"github.com/gtkit/gin-api/internal/shared/env"
	"go.opentelemetry.io/otel"
)

type middlewareConfiguration struct{ cfg *config.Config }

func (c middlewareConfiguration) Value() (*config.Config, error) { return c.cfg, nil }

type testMiddlewareOwner struct {
	registry *core.PluginRegistry
	builder  plugin.MiddlewareBuilder
	err      error
}

func (o testMiddlewareOwner) Build(deps plugin.MiddlewareDeps) (chain []gin.HandlerFunc, err error) {
	defer func() { err = errors.Join(err, o.registry.Stop(context.Background())) }()
	if o.err != nil {
		return nil, o.err
	}
	return o.builder.Build(deps)
}
func testMiddlewareWithOrigins(origins []string) plugin.MiddlewareBuilder {
	cfg := &config.Config{}
	cfg.CORS.AllowedOrigins = origins
	builder := defaulthttp.New(defaulthttp.Options{Configuration: middlewareConfiguration{cfg}})
	registry := core.NewPluginRegistry()
	err := registry.Register(builder)
	if err == nil {
		err = registry.Start(context.Background())
	}
	return testMiddlewareOwner{registry: registry, builder: builder, err: err}
}
func newRouterForTest(environment env.Env, opt Options) (*gin.Engine, error) {
	if opt.Middleware == nil {
		opt.Middleware = testMiddlewareWithOrigins(nil)
	}
	if opt.Metrics == nil {
		recorder, err := metrics.New(otel.Meter("router-contract"))
		if err != nil {
			return nil, err
		}
		opt.Metrics = recorder
	}
	return New(environment, opt)
}
