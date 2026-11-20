//go:build optional

package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/optional/outbound"
	"github.com/gtkit/gin-api/internal/optional/taskqueue"
	"github.com/gtkit/gin-api/internal/optional/upload"
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
	// Dir stores completed files for the local driver; required for local, no default.
	Dir string `mapstructure:"dir"`
	// ChunkDir stores temporary upload data; required when enabled, no default.
	ChunkDir string `mapstructure:"chunk_dir"`
	// MaxFileSize caps each uploaded file in bytes; must be positive when enabled.
	MaxFileSize int64 `mapstructure:"max_file_size"`
	// AllowedTypes is a nonempty file-extension allowlist when enabled; no default.
	AllowedTypes []string `mapstructure:"allowed_types"`
	// RedisDB is the logical DB for chunk sessions; must be listed in redis.dbs.
	RedisDB int `mapstructure:"redis_db"`
	// RequestTimeout bounds upload/merge routes in seconds; 0 uses 300s.
	RequestTimeout int `mapstructure:"request_timeout"`
	// Storage selects the single active storage driver and its connection params.
	Storage upload.StorageConfig `mapstructure:"storage"`
}

// EffectiveRequestTimeout returns the upload route timeout, defaulting to 300s.
func (c UploadConfig) EffectiveRequestTimeout() time.Duration {
	if c.RequestTimeout > 0 {
		return time.Duration(c.RequestTimeout) * time.Second
	}
	return 300 * time.Second
}

type AuditLogConfig struct {
	// Enabled creates the best-effort recorder and consumer; false by default.
	// Business routes and the database migration still require explicit registration.
	Enabled bool `mapstructure:"enabled"`
}

func (c *Config) TaskQueueEnabled() bool { return c != nil && c.Tasks.Enabled }

func (c *Config) validateOptional() error {
	return errors.Join(c.Tasks.Validate(c.Redis.Addr, c.Redis.DBs), c.Outbound.Validate(), c.validateUpload())
}

func (c *Config) validateUpload() error {
	if !c.Upload.Enabled {
		return nil
	}
	var errs []error
	if strings.TrimSpace(c.Upload.ChunkDir) == "" || c.Upload.MaxFileSize <= 0 || len(c.Upload.AllowedTypes) == 0 {
		errs = append(errs, errors.New("upload requires chunk_dir, positive max_file_size and allowed_types"))
	}
	if err := c.Upload.Storage.Validate(); err != nil {
		errs = append(errs, err)
	}
	if c.Upload.Storage.EffectiveDriver() == upload.DriverLocal && strings.TrimSpace(c.Upload.Dir) == "" {
		errs = append(errs, errors.New("upload storage driver local requires dir"))
	}
	if c.Upload.RequestTimeout < 0 {
		errs = append(errs, fmt.Errorf("upload.request_timeout %d must not be negative", c.Upload.RequestTimeout))
	}
	// 会话存储必须有 Redis，且逻辑库真的在 redis.dbs 里：
	// 不校验的话资源层取不到该库的客户端，装配期才失败。
	if strings.TrimSpace(c.Redis.Addr) == "" {
		errs = append(errs, errors.New("upload is enabled but redis.addr is empty: chunk sessions have no backing store"))
	}
	if c.Upload.RedisDB < 0 {
		errs = append(errs, fmt.Errorf("upload.redis_db %d must not be negative", c.Upload.RedisDB))
	} else if !slices.Contains(c.Redis.DBs, c.Upload.RedisDB) {
		errs = append(errs, fmt.Errorf("upload.redis_db %d is not listed in redis.dbs %v", c.Upload.RedisDB, c.Redis.DBs))
	}
	return errors.Join(errs...)
}
