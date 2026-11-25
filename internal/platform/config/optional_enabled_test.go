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
	for _, block := range []string{"upload:\n  enabled: true\n", "upload:\n  rate_limit_enabled: true\n", "tasks:\n  enabled: true\n  redis_db: 99\n", "outbound:\n  clients:\n    broken:\n      base_url: invalid\n"} {
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

func TestUploadValidatesItsRedisAddressAndDatabase(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Upload.Enabled = true
	cfg.Upload.RedisDB = 2
	cfg.Upload.Dir = "files"
	cfg.Upload.ChunkDir = "chunks"
	cfg.Upload.MaxFileSize = 1 << 20
	cfg.Upload.AllowedTypes = []string{".txt"}

	cfg.Redis.DBs = []int{2}
	if err := cfg.validateUpload(); err == nil || !strings.Contains(err.Error(), "redis.addr") {
		t.Fatalf("missing Redis address: %v", err)
	}
	cfg.Redis = RedisConfig{Addr: "127.0.0.1:6379", DBs: []int{0}}
	if err := cfg.validateUpload(); err == nil || !strings.Contains(err.Error(), "upload.redis_db") {
		t.Fatalf("unconfigured Redis logical DB: %v", err)
	}
	cfg.Redis.DBs = append(cfg.Redis.DBs, 2)
	if err := cfg.validateUpload(); err != nil {
		t.Fatal(err)
	}
}
