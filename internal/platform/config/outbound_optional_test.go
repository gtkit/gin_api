package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestOutboundConfigFollowsWiring(t *testing.T) {
	t.Parallel()
	_, wired := reflect.TypeFor[Config]().FieldByName("Outbound")
	for _, block := range []string{"outbound: {}\n", "outbound:\n  clients: {}\n", "outbound:\n  clients:\n    billing:\n      base_url: https://billing.example.test\n"} {
		t.Run(strings.TrimSpace(block), func(t *testing.T) {
			t.Parallel()
			_, err := loadFromReader(strings.NewReader(minimalConfigYAML() + "\n" + block))
			if wired {
				if err != nil {
					t.Fatalf("wired outbound configuration must load, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "decode config") || !strings.Contains(err.Error(), "outbound") {
				t.Fatalf("unwired outbound configuration must be rejected, got %v", err)
			}
		})
	}
}

func TestOutboundEnvironmentFollowsWiring(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	writeJWTCommandConfig(t, root, minimalConfigYAML())
	t.Setenv("APP_OUTBOUND__DEFAULTS__TIMEOUT", "3s")
	_, err := Load()
	if _, wired := reflect.TypeFor[Config]().FieldByName("Outbound"); wired {
		if err != nil {
			t.Fatalf("wired outbound environment must load, got %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "decode config") || !strings.Contains(err.Error(), "outbound") {
		t.Fatalf("unwired outbound environment must be rejected, got %v", err)
	}
}
