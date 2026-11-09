//go:build optional

package config

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/knadh/koanf/parsers/yaml"
)

func TestOptionalConfigDecodingAndValidation(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_OUTBOUND__DEFAULTS__TIMEOUT", "3s")
	cfg, err := loadFromReader(strings.NewReader(minimalConfigYAML() + "\n" + `tasks:
  enabled: true
  redis_db: 0
upload:
  enabled: false
auditlog:
  enabled: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TaskQueueEnabled() || !cfg.AuditLog.Enabled || cfg.Outbound.Defaults.Timeout.String() != "3s" || len(cfg.IgnoredKeys()) != 0 {
		t.Fatalf("optional config was not decoded: ignored=%v", cfg.IgnoredKeys())
	}
	for _, block := range []string{"upload:\n  enabled: true\n", "tasks:\n  enabled: true\n  redis_db: 99\n", "outbound:\n  clients:\n    broken:\n      base_url: invalid\n"} {
		if _, err := loadFromReader(strings.NewReader(minimalConfigYAML() + "\n" + block)); err == nil {
			t.Fatalf("invalid optional config accepted: %s", block)
		}
	}
}

func TestOptionalExampleCoversConfiguration(t *testing.T) {
	raw, err := os.ReadFile("../../../config/optional.yml.example")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := yaml.Parser().Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	collectYAMLPaths(parsed, "", paths)
	wanted := map[string]bool{}
	collectStructPaths(reflect.TypeFor[OptionalConfig](), "", wanted)
	for path := range wanted {
		if !paths[path] {
			t.Errorf("optional example is missing %s", path)
		}
	}
}
