package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-viper/mapstructure/v2"
	"github.com/gtkit/gin-api/internal/diagnostics"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/gtkit/json/v2"
	"github.com/knadh/koanf/parsers/yaml"
)

func installCommandConfiguration(t *testing.T, cfg *config.Config) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "APP_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	raw := map[string]any{}
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{Result: &raw, TagName: "mapstructure"})
	if err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(cfg); err != nil {
		t.Fatal(err)
	}
	delete(raw, "modules")
	data, err := yaml.Parser().Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "config", "env")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env.yml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_ROOT", root)
}

func TestDefaultDoctorLoadsConfigWithoutOpeningNotificationChannels(t *testing.T) {
	testutil.RequireLoopback(t)
	var requests atomic.Int32
	endpoint := httptest.NewTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	cfg := configWithRealKeysForDoctor(t)
	cfg.News = config.NewsConfig{FSURL: endpoint.URL, Platform: "feishu"}
	cfg.Log.Path = filepath.Join(t.TempDir(), "app")
	installCommandConfiguration(t, cfg)
	command := newDoctorCmd(nil)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--json"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("doctor=%v output=%s", err, output.String())
	}
	var report diagnostics.Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion == 0 || report.Deep || len(report.Checks) == 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if requests.Load() != 0 {
		t.Fatal("configuration-only doctor sent a notification")
	}
}
