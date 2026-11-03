package config

import (
	"strings"
	"testing"
)

func TestTaskQueueConfigFollowsWiring(t *testing.T) {
	t.Parallel()
	for _, block := range []string{"tasks: {}\n", "tasks:\n  enabled: false\n", "tasks:\n  enabled: true\n"} {
		t.Run(strings.TrimSpace(block), func(t *testing.T) {
			t.Parallel()
			_, err := loadFromReader(strings.NewReader(minimalConfigYAML() + "\n" + block))
			if err != nil {
				t.Fatalf("unused or wired configuration blocked loading: %v", err)
			}
		})
	}
}

func TestTaskQueueEnvironmentFollowsWiring(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	writeJWTCommandConfig(t, root, minimalConfigYAML())
	t.Setenv("APP_TASKS__ENABLED", "false")
	_, err := Load()
	if err != nil {
		t.Fatalf("unused or wired environment blocked loading: %v", err)
	}
}
