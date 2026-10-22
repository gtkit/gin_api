package robot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestSenderPreservesPlatformSigningAndPerCallTarget(t *testing.T) {
	for _, platform := range []string{"", " FeIsHu ", " DingTalk ", " WeCom "} {
		t.Run(platform, func(t *testing.T) {
			var paths []string
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["msg_type"] != "text" && body["msgtype"] != "text" {
					t.Errorf("message type=%v", body)
				}
				if strings.Contains(strings.ToLower(platform), "feishu") || platform == "" {
					if body["sign"] == nil || body["timestamp"] == nil {
						t.Errorf("missing feishu signature: %v", body)
					}
				}
				if strings.Contains(strings.ToLower(platform), "dingtalk") && r.URL.Query().Get("sign") == "" {
					t.Error("missing dingtalk signature")
				}
				_, _ = w.Write([]byte(`{"code":0,"errcode":0,"msg":"ok","errmsg":"ok"}`))
			}))
			s, cleanup, err := openSender(t.Context(), config.NewsConfig{Platform: platform, Secret: "test-secret"})
			if err != nil {
				t.Fatal(err)
			}
			if cleanup != nil {
				t.Fatal("default sender must not close shared transport")
			}
			concrete, ok := s.(*sender)
			if !ok {
				t.Fatal("default factory did not return its sender")
			}
			concrete.client = server.Client()
			for _, path := range []string{"/first", "/second"} {
				if err := s.Send(t.Context(), server.URL+path, "body"); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(paths, []string{"/first", "/second"}) {
				t.Fatalf("paths=%v", paths)
			}
		})
	}
}

func TestSenderUnknownPlatformIsRejectedAtConstruction(t *testing.T) {
	if _, _, err := openSender(t.Context(), config.NewsConfig{Platform: "unknown"}); err == nil {
		t.Fatal("unknown platform accepted")
	}
}
