//go:build optional

package cmd

import (
	"github.com/gtkit/gin-api/internal/optional/auditlog"
	"github.com/gtkit/gin-api/internal/platform/config"
)

func optionalDDLModels(cfg *config.Config) []any {
	if cfg != nil && cfg.AuditLog.Enabled {
		return []any{&auditlog.Record{}}
	}
	return nil
}
