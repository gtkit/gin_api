package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestFirstToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "nil args", args: nil, want: ""},
		{name: "empty args", args: []string{}, want: ""},
		{name: "only flags", args: []string{"-h", "--strict"}, want: ""},
		{name: "single token", args: []string{"up"}, want: "up"},
		{name: "flag then token", args: []string{"--strict", "lint"}, want: "lint"},
		{name: "token then flag", args: []string{"status", "--json"}, want: "status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := firstToken(tt.args); got != tt.want {
				t.Fatalf("firstToken(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestMigrateAndMakeCommandMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		build     func(*rootOptions) *cobra.Command
		wantUse   string
		wantShort string
	}{
		{
			name:      "migrate",
			build:     newMigrateCmd,
			wantUse:   "migrate",
			wantShort: "Run database migrations (up/down/status/lint/pretend)",
		},
		{
			name:      "make",
			build:     newMakeCmd,
			wantUse:   "make",
			wantShort: "Generate migration files from GORM models",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := tt.build(&rootOptions{})
			if cmd.Use != tt.wantUse {
				t.Fatalf("Use = %q, want %q", cmd.Use, tt.wantUse)
			}
			if cmd.Short != tt.wantShort {
				t.Fatalf("Short = %q, want %q", cmd.Short, tt.wantShort)
			}
			if !cmd.DisableFlagParsing {
				t.Fatalf("DisableFlagParsing = false, want true")
			}
		})
	}
}

// TestMigrateSubtreeHelpDoesNotTouchDB 验证无参与 help 走 cmd.Help()，不初始化运行时、不连库。
func TestMigrateSubtreeHelpDoesNotTouchDB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		build func(*rootOptions) *cobra.Command
		args  []string
	}{
		{name: "migrate no args", build: newMigrateCmd, args: []string{}},
		{name: "migrate help", build: newMigrateCmd, args: []string{"help"}},
		{name: "make no args", build: newMakeCmd, args: []string{}},
		{name: "make help", build: newMakeCmd, args: []string{"help"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := tt.build(&rootOptions{})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tt.args)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error = %v, want nil", err)
			}
			if !strings.Contains(out.String(), "Usage:") {
				t.Fatalf("expected help output, got: %s", out.String())
			}
		})
	}
}

func TestRootRegistersMigrationCommands(t *testing.T) {
	t.Parallel()

	rootCmd := NewRootCmd(nil)
	want := map[string]bool{"migrate": true, "make": true}

	got := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		got[cmd.Name()] = true
	}

	for name := range want {
		if !got[name] {
			t.Fatalf("command %q is not registered", name)
		}
	}
}

func TestMigrationExecutionPreservesContextAndBothErrors(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "migration"))
	cancel()
	execution, closing := errors.New("DDL failed"), errors.New("database close failed")
	closeCalls := 0
	sub := &cobra.Command{Use: "up", RunE: func(cmd *cobra.Command, _ []string) error {
		if cmd.Context() != ctx || cmd.Context().Value(key{}) != "migration" || !errors.Is(cmd.Context().Err(), context.Canceled) {
			t.Error("migration command lost context")
		}
		return execution
	}}
	err := executeMigrationCommand(ctx, sub, nil, func() error { closeCalls++; return closing }, "run migrate")
	if !errors.Is(err, execution) || !errors.Is(err, closing) || closeCalls != 1 {
		t.Fatalf("command error=%v close calls=%d", err, closeCalls)
	}
}
