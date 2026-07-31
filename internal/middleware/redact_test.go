package middleware

import (
	"strings"
	"testing"
)

func TestIsSensitiveKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "exact password", key: "password", want: true},
		{name: "exact case-insensitive", key: "PassWord", want: true},
		{name: "exact sms_code", key: "sms_code", want: true},
		{name: "exact code", key: "code", want: true},
		{name: "stem in compound", key: "user_password", want: true},
		{name: "access_token stem", key: "accessToken", want: true},
		{name: "client secret stem", key: "client_secret", want: true},
		{name: "authorization", key: "authorization", want: true},
		{name: "phone", key: "phone", want: true},
		{name: "non-sensitive zipcode not matched by code", key: "zipcode", want: false},
		{name: "non-sensitive countrycode not matched by code", key: "countrycode", want: false},
		{name: "plain name", key: "name", want: false},
		{name: "empty", key: "", want: false},
		{name: "whitespace", key: "   ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isSensitiveKey(tt.key); got != tt.want {
				t.Fatalf("isSensitiveKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestRedactQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		mustContain  []string
		mustAbsent   []string
		wantExactOut string
	}{
		{
			name:        "redacts token keeps benign",
			raw:         "token=abc123&page=2",
			mustContain: []string{"token=" + urlEscape(redactedPlaceholder), "page=2"},
			mustAbsent:  []string{"abc123"},
		},
		{
			name:        "redacts multiple sensitive",
			raw:         "password=secretpw&code=8888&name=bob",
			mustContain: []string{"name=bob"},
			mustAbsent:  []string{"secretpw", "8888"},
		},
		{
			name:         "empty query",
			raw:          "",
			wantExactOut: "",
		},
		{
			name:         "unparseable query is not echoed",
			raw:          "%zz",
			wantExactOut: "[unparseable query]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := redactQuery(tt.raw)
			if tt.wantExactOut != "" || tt.raw == "" {
				if got != tt.wantExactOut {
					t.Fatalf("redactQuery(%q) = %q, want %q", tt.raw, got, tt.wantExactOut)
				}
				return
			}
			for _, sub := range tt.mustContain {
				if !strings.Contains(got, sub) {
					t.Errorf("redactQuery(%q) = %q, must contain %q", tt.raw, got, sub)
				}
			}
			for _, sub := range tt.mustAbsent {
				if strings.Contains(got, sub) {
					t.Errorf("redactQuery(%q) = %q, must NOT contain %q", tt.raw, got, sub)
				}
			}
		})
	}
}

func TestRedactBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		raw         string
		mustContain []string
		mustAbsent  []string
		wantExact   string
	}{
		{
			name:        "json redacts nested and array",
			contentType: "application/json; charset=utf-8",
			// 密文用独特标记，避免与合法保留的字段名（pwd/token）子串相撞。
			raw:         `{"password":"SECRET_A","name":"bob","nested":{"token":"SECRET_B"},"list":[{"pwd":"SECRET_C"}]}`,
			mustContain: []string{`"name":"bob"`, redactedPlaceholder},
			mustAbsent:  []string{"SECRET_A", "SECRET_B", "SECRET_C"},
		},
		{
			name:        "form urlencoded redacts",
			contentType: "application/x-www-form-urlencoded",
			raw:         "username=bob&password=pw",
			mustContain: []string{"username=bob"},
			mustAbsent:  []string{"pw"},
		},
		{
			name:        "invalid json falls back to placeholder",
			contentType: "application/json",
			raw:         `{not-json`,
			mustContain: []string{"application/json", "bytes"},
			mustAbsent:  []string{"not-json"},
		},
		{
			name:        "binary content type placeholder only",
			contentType: "application/octet-stream",
			raw:         "\x00\x01rawsecretbytes",
			mustContain: []string{"application/octet-stream", "bytes"},
			mustAbsent:  []string{"rawsecretbytes"},
		},
		{
			name:        "empty body",
			contentType: "application/json",
			raw:         "",
			wantExact:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := redactBody(tt.contentType, []byte(tt.raw))
			if tt.raw == "" {
				if got != tt.wantExact {
					t.Fatalf("redactBody empty = %q, want %q", got, tt.wantExact)
				}
				return
			}
			for _, sub := range tt.mustContain {
				if !strings.Contains(got, sub) {
					t.Errorf("redactBody = %q, must contain %q", got, sub)
				}
			}
			for _, sub := range tt.mustAbsent {
				if strings.Contains(got, sub) {
					t.Errorf("redactBody = %q, must NOT contain %q (leak)", got, sub)
				}
			}
		})
	}
}

// urlEscape 复刻 url.Values.Encode 对占位符的转义，便于断言。
func urlEscape(s string) string {
	replacer := strings.NewReplacer("[", "%5B", "]", "%5D")
	return replacer.Replace(s)
}
