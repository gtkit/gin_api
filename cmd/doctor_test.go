package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestDoctorCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		load       configLoader
		wantErr    bool
		wantOutput []string
	}{
		{
			name: "valid config",
			load: func() (*config.Config, error) {
				return &config.Config{
					Env: "test",
					App: config.AppConfig{Name: "app_client", Host: "127.0.0.1", Port: "8008"},
				}, nil
			},
			wantOutput: []string{
				"config: ok",
				"env: test",
				"application: app_client",
				"listen: 127.0.0.1:8008",
				"pprof: disabled",
			},
		},
		{
			name: "load error",
			load: func() (*config.Config, error) {
				return nil, errors.New("invalid yaml")
			},
			wantErr: true,
		},
		{
			name: "nil config",
			load: func() (*config.Config, error) {
				return nil, nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := newDoctorCmdWithLoader(tt.load)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)

			err := cmd.Execute()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Execute() error = %v, wantErr %v", err, tt.wantErr)
			}
			for _, want := range tt.wantOutput {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output missing %q: %s", want, out.String())
				}
			}
			if strings.Contains(out.String(), "password") || strings.Contains(out.String(), "secret") {
				t.Fatalf("output contains a sensitive field name: %s", out.String())
			}
		})
	}
}

func TestEnabledLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		enabled bool
		want    string
	}{
		{name: "enabled", enabled: true, want: "enabled"},
		{name: "disabled", enabled: false, want: "disabled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := enabledLabel(tt.enabled); got != tt.want {
				t.Fatalf("enabledLabel(%v) = %q, want %q", tt.enabled, got, tt.want)
			}
		})
	}
}
