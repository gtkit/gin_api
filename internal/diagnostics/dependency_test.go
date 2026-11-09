package diagnostics

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestNamedDependenciesAreNeverOmitted(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		DatabaseConnections: map[string]config.DatabaseConfig{"reports": {}, "archive": {}},
		RedisConnections:    map[string]config.RedisConfig{"jobs": {}},
	}
	opts := Options{Deep: true, Probes: Probes{
		NamedDatabases: map[string]func(context.Context) error{
			"reports": func(context.Context) error { return errors.New("SQL denied: password=secret") },
		},
		NamedRedis: map[string]func(context.Context) error{"jobs": func(context.Context) error { return nil }},
	}}
	want := map[string]string{"database[reports]": CodeProbeFailed, "database[archive]": CodeUnreachable, "redis[jobs]": CodeOK}
	for _, check := range buildChecks(cfg, opts) {
		code, ok := want[check.name]
		if !ok {
			continue
		}
		got := check.run(t.Context())
		if got.ErrorCode != code || strings.Contains(got.Detail, "secret") {
			t.Errorf("%s: %+v", check.name, got)
		}
		if code != CodeOK && got.Status != StatusFail {
			t.Errorf("required named connection did not fail: %+v", got)
		}
		delete(want, check.name)
	}
	if len(want) != 0 {
		t.Fatalf("named checks were omitted: %v", want)
	}
	for _, check := range buildChecks(cfg, Options{}) {
		if strings.Contains(check.name, "[") && check.run(t.Context()).Status != StatusSkipped {
			t.Errorf("shallow check performed I/O: %s", check.name)
		}
	}
}

func TestProbeFailureClassificationDoesNotExposeDetails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"query", errors.New("version query failed: password=secret"), CodeProbeFailed},
		{"network", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("secret")}, CodeUnreachable},
		{"permission", os.ErrPermission, CodePermission},
		{"deadline", context.DeadlineExceeded, CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkProbe(t.Context(), Options{Deep: true}, func(context.Context) error { return tc.err }, true)
			if got.ErrorCode != tc.code || got.Status != StatusFail || strings.Contains(got.Detail, "secret") {
				t.Fatalf("unexpected diagnostic: %+v", got)
			}
		})
	}
}
