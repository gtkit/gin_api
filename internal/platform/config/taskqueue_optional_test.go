package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestTaskQueueConfigFollowsWiring(t *testing.T) {
	t.Parallel()
	_, wired := reflect.TypeFor[Config]().FieldByName("Tasks")
	for _, block := range []string{"tasks: {}\n", "tasks:\n  enabled: false\n", "tasks:\n  enabled: true\n"} {
		t.Run(strings.TrimSpace(block), func(t *testing.T) {
			t.Parallel()
			_, err := loadFromReader(strings.NewReader(minimalConfigYAML() + "\n" + block))
			if wired {
				if err != nil {
					t.Fatalf("wired tasks configuration must load, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "decode config") || !strings.Contains(err.Error(), "tasks") {
				t.Fatalf("unwired tasks configuration must be rejected, got %v", err)
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
	if _, wired := reflect.TypeFor[Config]().FieldByName("Tasks"); wired {
		if err != nil {
			t.Fatalf("wired tasks environment must load, got %v", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "decode config") || !strings.Contains(err.Error(), "tasks") {
		t.Fatalf("unwired tasks environment must be rejected, got %v", err)
	}
}
