package robot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/news"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
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
				if _, err := w.Write([]byte(`{"code":0,"errcode":0,"msg":"ok","errmsg":"ok"}`)); err != nil {
					t.Error(err)
				}
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
				if err := s.SendContext(t.Context(), server.URL+path, "body"); err != nil {
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

func TestRobotPlugsDirectlyIntoLogger(t *testing.T) {
	for _, owned := range []bool{false, true} {
		name := "sender"
		if owned {
			name = "owner"
		}
		t.Run(name, func(t *testing.T) {
			paths := make(chan string, 2)
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				if _, err := w.Write([]byte(`{"code":0}`)); err != nil {
					t.Error(err)
				}
			}))
			client := server.Client()
			cfg := &config.Config{News: config.NewsConfig{FSURL: server.URL + "/default"}}
			open := func(ctx context.Context, cfg config.NewsConfig) (news.WebhookSender, func(context.Context) error, error) {
				result, cleanup, err := openSender(ctx, cfg)
				if err == nil {
					concrete, ok := result.(*sender)
					if !ok {
						t.Fatalf("unexpected sender type %T", result)
					}
					concrete.client = client
				}
				return result, cleanup, err
			}
			var webhook news.WebhookSender
			var owner *Owner
			var err error
			if owned {
				owner, err = Open(t.Context(), Options{Config: cfg, Open: open})
				webhook = owner
				if owner != nil {
					t.Cleanup(func() {
						if err := owner.Close(context.WithoutCancel(t.Context())); err != nil {
							t.Error(err)
						}
					})
				}
			} else {
				webhook, _, err = open(t.Context(), cfg.News)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := webhook.SendContext(t.Context(), "", "default preflight"); err != nil {
				t.Fatalf("default target before SDK: %v", err)
			}
			if got := <-paths; got != "/default" {
				t.Fatalf("default target path=%q", got)
			}
			instance := logger.MustNew(logger.WithConsole(true), logger.WithFile(false), logger.WithReplaceGlobals(false), logger.WithMessager(webhook))
			t.Cleanup(instance.Sync)
			instance.HError("default body")
			instance.HErrorTo(server.URL+"/explicit", "explicit body")
			instance.Sync()
			for _, want := range []string{"/default", "/explicit"} {
				select {
				case got := <-paths:
					if got != want {
						t.Fatalf("path=%q want=%q", got, want)
					}
				default:
					t.Fatal("SDK did not deliver to the HTTP endpoint")
				}
			}
			if owned {
				if err := owner.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := owner.SendContext(t.Context(), "", "late"); !errors.Is(err, news.ErrStopped) {
					t.Fatalf("send after close=%v", err)
				}
				owner.Send("late SDK call")
			} else {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if err := webhook.SendContext(ctx, "", "canceled"); !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled send=%v", err)
				}
			}
			if len(paths) != 0 {
				t.Fatal("canceled or stopped sender started another request")
			}
		})
	}
}
