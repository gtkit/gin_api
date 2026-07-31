package bootstrap

import (
	"fmt"
	"sync/atomic"

	gojwt "github.com/gtkit/gojwt"

	"github.com/gtkit/gin-api/internal/platform/config"
	legacydao "github.com/gtkit/gin-api/internal/platform/data"
)

// Runtime represents process-wide runtime dependencies.
type Runtime struct {
	config      *config.Config
	db          legacydao.Dao
	tokenIssuer *gojwt.JwtEd25519
	closed      atomic.Bool
}

// NewRuntime builds runtime dependencies that can be reused by commands.
func NewRuntime(opts Options) (*Runtime, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	if err := initLegacyRuntime(cfg, opts); err != nil {
		return nil, err
	}

	db := legacydao.DB()
	if db == nil {
		closeLegacyRuntime()
		return nil, fmt.Errorf("legacy dao is not initialized")
	}

	// JWT issuer 只在此处创建一次（旧实现 initLegacyRuntime 预检创建一份又丢弃、
	// 这里再经 JwtEd25519() 重建一份，属冗余）。现统一创建并复用。
	tokenIssuer, err := initJWTWithConfig(cfg)
	if err != nil {
		closeLegacyRuntime()
		return nil, fmt.Errorf("init jwt: %w", err)
	}
	if tokenIssuer == nil {
		closeLegacyRuntime()
		return nil, fmt.Errorf("jwt issuer is nil")
	}

	return &Runtime{
		config:      cfg,
		db:          db,
		tokenIssuer: tokenIssuer,
	}, nil
}

// Config returns the typed runtime configuration.
func (r *Runtime) Config() *config.Config {
	return r.config
}

// DB returns the legacy DAO while runtime wiring is still migrating.
func (r *Runtime) DB() legacydao.Dao {
	return r.db
}

// TokenIssuer returns the initialized JWT issuer for explicit injection.
func (r *Runtime) TokenIssuer() *gojwt.JwtEd25519 {
	if r == nil {
		return nil
	}

	return r.tokenIssuer
}

// Close releases runtime resources exactly once.
func (r *Runtime) Close() {
	if r == nil || !r.closed.CompareAndSwap(false, true) {
		return
	}

	closeLegacyRuntime()
}
