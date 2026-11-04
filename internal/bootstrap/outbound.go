//go:build optional

package bootstrap

import (
	"context"
	"fmt"

	"github.com/gtkit/gin-api/internal/optional/outbound"
	"github.com/gtkit/logger/v2"
)

func buildOutbound(inf *infra, cleanup *providerCleanup) error {
	downstreams, err := outbound.NewDownstreams(inf.cfg.Outbound)
	if err != nil {
		return fmt.Errorf("build outbound downstreams: %w", err)
	}
	inf.outbound = downstreams
	cleanup.push("outbound http clients", cleanupExtensions, func(context.Context) error {
		return downstreams.Close()
	})
	if names := downstreams.Names(); len(names) > 0 {
		logger.Infof("outbound downstreams assembled with circuit breakers: %v", names)
	}
	return nil
}
