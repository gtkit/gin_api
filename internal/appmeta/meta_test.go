package appmeta

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNoBusinessPollution(t *testing.T) {
	t.Parallel()

	externalURL := regexp.MustCompile(`https?://`)
	data, err := os.ReadFile("meta.go")
	if err != nil {
		t.Fatalf("read meta.go: %v", err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") && externalURL.MatchString(trimmed) {
			t.Errorf("appmeta must not contain external URLs, got %q", trimmed)
		}
	}
}

func TestEmbeddedIdentity(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("identity.mk")
	if err != nil {
		t.Fatalf("read identity.mk: %v", err)
	}
	want, err := ParseIdentity(string(data))
	if err != nil {
		t.Fatalf("parse identity.mk: %v", err)
	}
	if CommandName() != want.CommandName || RuntimeName() != want.RuntimeName {
		t.Fatalf("embedded identity = %q, %q, want %+v", CommandName(), RuntimeName(), want)
	}
}

func TestIdentityParsersAgree(t *testing.T) {
	t.Parallel()

	const valid = "APP_NAME = demo_api\nAPP_DISPLAY_NAME = Demo API-2.0\n"
	tests := []struct {
		name   string
		source string
		want   Identity
	}{
		{"literals", "# Identity\n\n" + valid, Identity{"demo_api", "Demo API-2.0"}},
		{"single character and CRLF", "APP_NAME = a\r\nAPP_DISPLAY_NAME = A\r\n", Identity{"a", "A"}},
		{"maximum lengths", "APP_NAME = " + strings.Repeat("a", 63) + "\nAPP_DISPLAY_NAME = " + strings.Repeat("B", 64), Identity{strings.Repeat("a", 63), strings.Repeat("B", 64)}},
		{"missing key", "APP_NAME = demo_api\n", Identity{}},
		{"empty name", "APP_NAME = \nAPP_DISPLAY_NAME = Demo API\n", Identity{}},
		{"duplicate key", valid + "APP_NAME = other\n", Identity{}},
		{"make directive", valid + "include another.mk\n", Identity{}},
		{"make expression", "APP_NAME = $(APP_DISPLAY_NAME)\nAPP_DISPLAY_NAME = Demo API\n", Identity{}},
		{"comment continuation", "# continued\\\n" + valid, Identity{}},
		{"trailing space", "APP_NAME = demo_api\nAPP_DISPLAY_NAME = Demo API \n", Identity{}},
		{"business URL", "APP_NAME = demo_api\nAPP_DISPLAY_NAME = https://example.com\n", Identity{}},
		{"long name", "APP_NAME = " + strings.Repeat("a", 64) + "\nAPP_DISPLAY_NAME = Demo API\n", Identity{}},
		{"long display name", "APP_NAME = demo_api\nAPP_DISPLAY_NAME = " + strings.Repeat("B", 65) + "\n", Identity{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "identity.mk")
			if err := os.WriteFile(path, []byte(tt.source), 0o600); err != nil {
				t.Fatal(err)
			}
			identity, err := ParseIdentity(tt.source)
			out, shellErr := exec.CommandContext(t.Context(), "sh", "../../scripts/check-appmeta.sh", path).Output()
			if tt.want == (Identity{}) {
				if err == nil || shellErr == nil || len(out) != 0 {
					t.Fatalf("expected both readers to reject input, Go error = %v, shell = %q, %v", err, out, shellErr)
				}
				return
			}
			if err != nil || identity != tt.want {
				t.Fatalf("ParseIdentity = %+v, %v, want %+v", identity, err, tt.want)
			}
			if shellErr != nil || string(out) != tt.want.CommandName+"\n" {
				t.Fatalf("shell reader = %q, %v, want %q", out, shellErr, tt.want.CommandName+"\n")
			}
		})
	}
}
