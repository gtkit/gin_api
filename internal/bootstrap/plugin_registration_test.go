package bootstrap

import (
	"context"
	"testing"

	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestRegisterDefaultPluginsReturnsTypedCapabilities(t *testing.T) {
	t.Parallel()

	registry := core.NewPluginRegistry()
	set, err := RegisterDefaultPlugins(context.Background(), &config.Config{}, registry, PluginRegistrationOptions{})
	if err != nil {
		t.Fatalf("RegisterDefaultPlugins: %v", err)
	}
	if set.Database == nil || set.Database.Name() != "mysql" {
		t.Fatalf("database plugin = %#v, want mysql", set.Database)
	}
	if set.Redis == nil || set.Redis.Name() != "redis" {
		t.Fatalf("redis plugin = %#v, want redis", set.Redis)
	}
	if set.Cache == nil || set.Cache.Name() != "cache" {
		t.Fatalf("cache plugin = %#v, want cache", set.Cache)
	}
}

func TestRegisterDefaultPluginsRequiresConfigAndRegistry(t *testing.T) {
	t.Parallel()

	if _, err := RegisterDefaultPlugins(context.Background(), nil, core.NewPluginRegistry(), PluginRegistrationOptions{}); err == nil {
		t.Fatal("nil config accepted")
	}
	if _, err := RegisterDefaultPlugins(context.Background(), &config.Config{}, nil, PluginRegistrationOptions{}); err == nil {
		t.Fatal("nil registry accepted")
	}
}
