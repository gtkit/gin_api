package redis

import (
	"context"
	"errors"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestRedisPluginOptionalFailureDegrades(t *testing.T) {
	t.Parallel()

	openErr := errors.New("redis unavailable")
	plugin := New(Options{Configuration: redisTestConfiguration{config.RedisConfig{Addr: "redis:6379"}},

		Open: func(context.Context, config.RedisConfig) (Client, error) {
			return nil, openErr
		},
	})
	if err := plugin.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !plugin.Configured() || plugin.DefaultClient() != nil {
		t.Fatal("optional failure did not preserve configured/degraded state")
	}
	if err := plugin.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func TestRedisPluginRequiredFailureIsReturned(t *testing.T) {
	t.Parallel()

	openErr := errors.New("redis unavailable")
	plugin := New(Options{Configuration: redisTestConfiguration{config.RedisConfig{Addr: "redis:6379"}},
		Required: func(*config.Config) bool { return true },
		Open: func(context.Context, config.RedisConfig) (Client, error) {
			return nil, openErr
		},
	})
	if err := plugin.Init(t.Context()); !errors.Is(err, openErr) {
		t.Fatalf("Init error = %v, want open error", err)
	}
	if err := plugin.Start(t.Context()); err == nil {
		t.Fatal("Start accepted a required plugin without a client")
	}
}

func TestRedisPluginUnavailableAccessorsAreSafe(t *testing.T) {
	t.Parallel()

	plugin := New(Options{Configuration: redisTestConfiguration{}})
	if plugin.Name() != "redis" {
		t.Fatalf("Name() = %q, want redis", plugin.Name())
	}
	if plugin.Client(0) != nil {
		t.Fatal("Client returned a handle before Init")
	}
	if client, ok := plugin.GetClient(0); client != nil || ok {
		t.Fatalf("GetClient = (%v, %t), want (nil, false)", client, ok)
	}
	if plugin.PoolStats() != nil {
		t.Fatal("PoolStats returned data before Init")
	}
	if plugin.DefaultClient() != nil {
		t.Fatal("DefaultClient returned a handle before Init")
	}
	if err := plugin.HealthCheck(t.Context()); err == nil {
		t.Fatal("HealthCheck accepted an unavailable plugin")
	}
	if plugin.Configured() {
		t.Fatal("unconfigured plugin reported Configured")
	}
	if err := plugin.Stop(t.Context()); err != nil {
		t.Fatalf("Stop without a client: %v", err)
	}
}

func TestRedisPluginUnconfiguredRequiredAndCanceled(t *testing.T) {
	t.Parallel()

	required := New(Options{Configuration: redisTestConfiguration{}, Required: func(*config.Config) bool { return true }})
	if err := required.Init(t.Context()); err == nil {
		t.Fatal("required unconfigured Redis accepted")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	optional := New(Options{Configuration: redisTestConfiguration{}})
	if err := optional.Init(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Init error = %v, want context.Canceled", err)
	}
}

type redisTestConfiguration struct{ redis config.RedisConfig }

func (c redisTestConfiguration) Value() (*config.Config, error) {
	return &config.Config{Redis: c.redis}, nil
}
