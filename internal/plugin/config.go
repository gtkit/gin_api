package plugin

import (
	"errors"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// ErrConfigurationUnavailable means no validated configuration was published.
var ErrConfigurationUnavailable = errors.New("plugin configuration is unavailable")

// ConfigurationProvider supplies the validated startup snapshot. Consumers must
// treat the returned configuration as read-only. It grants no lifecycle rights.
type ConfigurationProvider interface {
	Value() (*config.Config, error)
}
