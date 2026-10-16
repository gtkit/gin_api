package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/diagnostics"
	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestDeepDoctorPreservesIndependentResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := configWithRealKeysForDoctor(t)
		loads := 0
		cmd := newDoctorCmdWithDeps(func() (*config.Config, error) { loads++; return cfg, nil },
			func(_ context.Context, actual *config.Config) (diagnostics.Probes, func() error, error) {
				if actual != cfg {
					t.Fatal("probe builder did not receive loaded config")
				}
				return diagnostics.Probes{
					PingDB: func(ctx context.Context) error {
						if _, ok := ctx.Deadline(); !ok {
							t.Error("database probe has no deadline")
						}
						return nil
					},
					PingRedis: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
				}, func() error { return nil }, nil
			})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--deep", "--json", "--check-timeout", "10ms"})
		start := time.Now()
		if err := cmd.ExecuteContext(t.Context()); err == nil {
			t.Fatal("expected degraded report")
		}
		var report diagnostics.Report
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		got := map[string]diagnostics.CheckResult{}
		for _, check := range report.Checks {
			got[check.Name] = check
		}
		if loads != 1 || got["database"].Status != diagnostics.StatusPass || got["redis"].ErrorCode != diagnostics.CodeTimeout {
			t.Fatalf("loads=%d checks=%+v", loads, got)
		}
		if elapsed := time.Since(start); elapsed != 10*time.Millisecond {
			t.Fatalf("elapsed=%s, want single probe budget", elapsed)
		}
	})
}

func TestRuntimeProbesAreLazyAndIgnoreJWT(t *testing.T) {
	cfg := validConfigForDoctor()
	cfg.JWT.Active.PrivateKeyPath = "missing-private-key"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	probes, release, err := buildRuntimeProbes(ctx, cfg)
	if err != nil {
		t.Fatalf("building probe closures touched runtime: %v", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	}()
	for _, probe := range []func(context.Context) error{probes.PingDB, probes.PingRedis} {
		if err := probe(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled probe = %v", err)
		}
	}
}

func TestDeepDoctorSkipsUnconfiguredRedis(t *testing.T) {
	cfg := configWithRealKeysForDoctor(t)
	cfg.Redis = config.RedisConfig{}
	cmd := newDoctorCmdWithDeps(func() (*config.Config, error) { return cfg, nil },
		func(context.Context, *config.Config) (diagnostics.Probes, func() error, error) {
			return diagnostics.Probes{
				PingDB:    func(context.Context) error { return nil },
				PingRedis: func(context.Context) error { t.Error("unconfigured redis probed"); return nil },
			}, func() error { return nil }, nil
		})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--deep"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}
