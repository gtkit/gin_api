package router

import (
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/shared/env"
	"go.opentelemetry.io/otel"
)

func newRouterForTest(environment env.Env, opt Options) (*gin.Engine, error) {
	if opt.Metrics == nil {
		recorder, err := metrics.New(otel.Meter("router-contract"))
		if err != nil {
			return nil, err
		}
		opt.Metrics = recorder
	}
	return New(environment, opt)
}
