//go:build optional

package cmd

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gtkit/gin-api/internal/diagnostics"
	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestDeepDoctorTreatsUploadRedisAsRequired(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		enabled bool
		exit    int
	}{
		{name: "disabled", exit: diagnostics.ExitDegraded},
		{name: "enabled", enabled: true, exit: diagnostics.ExitFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configWithRealKeysForDoctor(t)
			cfg.Upload.Enabled = tc.enabled
			cfg.Upload.RedisDB = cfg.Redis.DBs[0]
			cfg.Upload.Dir = t.TempDir()
			cfg.Upload.ChunkDir = t.TempDir()
			cfg.Upload.MaxFileSize = 1 << 20
			cfg.Upload.AllowedTypes = []string{".txt"}
			cmd := newDoctorCmdWithDeps(func() (*config.Config, error) { return cfg, cfg.Validate() },
				func(context.Context, *config.Config) (diagnostics.Probes, func() error, error) {
					return diagnostics.Probes{
						PingDB:    func(context.Context) error { return nil },
						PingRedis: func(context.Context) error { return errors.New("redis unavailable") },
					}, nil, nil
				})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--deep"})
			err := cmd.ExecuteContext(t.Context())
			coded, ok := errors.AsType[*exitCodeError](err)
			if !ok || coded.ExitCode() != tc.exit {
				t.Fatalf("doctor error = %v, want exit code %d", err, tc.exit)
			}
		})
	}
}
