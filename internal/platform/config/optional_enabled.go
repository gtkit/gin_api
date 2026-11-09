//go:build optional

package config

import (
	"errors"
	"strings"

	"github.com/gtkit/gin-api/internal/optional/outbound"
	"github.com/gtkit/gin-api/internal/optional/taskqueue"
)

// OptionalConfig is decoded at the root only in builds with the optional tag.
type OptionalConfig struct {
	Tasks    taskqueue.Config `mapstructure:"tasks"`
	Outbound outbound.Config  `mapstructure:"outbound"`
	Upload   UploadConfig     `mapstructure:"upload"`
	AuditLog AuditLogConfig   `mapstructure:"auditlog"`
}

type UploadConfig struct {
	// Enabled creates an uploader; false by default, never registers public routes.
	Enabled bool `mapstructure:"enabled"`
	// Dir stores completed files; required when enabled, no default.
	Dir string `mapstructure:"dir"`
	// ChunkDir stores temporary upload data; required when enabled, no default.
	ChunkDir string `mapstructure:"chunk_dir"`
	// MaxFileSize caps each uploaded file in bytes; must be positive when enabled.
	MaxFileSize int64 `mapstructure:"max_file_size"`
	// AllowedTypes is a nonempty file-extension allowlist when enabled; no default.
	AllowedTypes []string `mapstructure:"allowed_types"`
}

type AuditLogConfig struct {
	// Enabled creates the best-effort recorder and consumer; false by default.
	// Business routes and the database migration still require explicit registration.
	Enabled bool `mapstructure:"enabled"`
}

func (c *Config) TaskQueueEnabled() bool { return c != nil && c.Tasks.Enabled }

func (c *Config) validateOptional() error {
	var uploadErr error
	if c.Upload.Enabled && (strings.TrimSpace(c.Upload.Dir) == "" || strings.TrimSpace(c.Upload.ChunkDir) == "" || c.Upload.MaxFileSize <= 0 || len(c.Upload.AllowedTypes) == 0) {
		uploadErr = errors.New("upload requires dir, chunk_dir, positive max_file_size and allowed_types")
	}
	return errors.Join(c.Tasks.Validate(c.Redis.Addr, c.Redis.DBs), c.Outbound.Validate(), uploadErr)
}
