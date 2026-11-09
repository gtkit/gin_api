package config

import (
	"strings"
	"testing"
)

func TestOutboundConfigFollowsWiring(t *testing.T) {
	t.Parallel()
	for _, block := range []string{"outbound: {}\n", "outbound:\n  clients: {}\n", "outbound:\n  clients:\n    billing:\n      base_url: https://billing.example.test\n"} {
		t.Run(strings.TrimSpace(block), func(t *testing.T) {
			t.Parallel()
			_, err := loadFromReader(strings.NewReader(minimalConfigYAML() + "\n" + block))
			if err != nil {
				t.Fatalf("unused or wired configuration blocked loading: %v", err)
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
	if err != nil {
		t.Fatalf("unused or wired environment blocked loading: %v", err)
	}
}
