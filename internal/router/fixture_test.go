package router

import (
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/shared/env"
	"github.com/gtkit/logger/v2"
	"go.opentelemetry.io/otel"
	"testing"
)

func newRouterForTest(t *testing.T, environment env.Env, opt Options) (*gin.Engine, error) {
	if opt.Metrics == nil {
		recorder, err := metrics.New(otel.Meter("router-contract"))
		if err != nil {
			return nil, err
		}
		opt.Metrics = recorder
	}
	if opt.AccessLogger == nil {
		opt.AccessLogger = logger.MustNew(logger.WithConsole(true), logger.WithFile(false), logger.WithReplaceGlobals(false))
		t.Cleanup(opt.AccessLogger.Sync)
	}
	return New(environment, opt)
}
