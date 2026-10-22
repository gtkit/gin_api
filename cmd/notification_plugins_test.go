package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gtkit/gin-api/internal/core"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	defaultconfig "github.com/gtkit/gin-api/internal/plugin/builtin/config"
	"github.com/gtkit/gin-api/internal/testutil"
)

func TestSharedNotificationRegistrationSendsThroughDefaultRobot(t *testing.T) {
	testutil.RequireLoopback(t)
	var mu sync.Mutex
	var messages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Error(err)
		}
		mu.Lock()
		messages = append(messages, message.Content.Text)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"code":0,"msg":"success"}`))
	}))
	t.Cleanup(server.Close)
	var loads int
	configuration := defaultconfig.New(func(context.Context) (*config.Config, error) {
		loads++
		cfg := validConfigForDoctor()
		cfg.Log.ConsoleStdout = 1
		cfg.News.FSURL = server.URL
		return cfg, nil
	})
	registry := core.NewPluginRegistry()
	if err := registry.Register(configuration); err != nil {
		t.Fatal(err)
	}
	logging, err := registerNotificationLogging(registry, configuration)
	if err != nil {
		t.Fatal(err)
	}
	if loads != 0 {
		t.Fatal("registration read configuration")
	}
	if err := registry.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Stop(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	applog.HErrorf("shared entry alert %d", 7)
	if err := logging.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if loads != 1 {
		t.Fatalf("configuration loads=%d", loads)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messages) != 1 || !strings.Contains(messages[0], "shared entry alert 7") {
		t.Fatalf("messages=%q", messages)
	}
}

func TestSharedNotificationGraphRejectsMissingAndCyclicConfiguration(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "cycle"}[cycle], func(t *testing.T) {
			loads := 0
			configuration := defaultconfig.New(func(context.Context) (*config.Config, error) { loads++; return nil, nil })
			registry := core.NewPluginRegistry()
			if cycle {
				if err := registry.Register(configuration, "logging"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := registerNotificationLogging(registry, configuration); err != nil {
				t.Fatal(err)
			}
			want := core.ErrPluginDependencyMissing
			if cycle {
				want = core.ErrPluginDependencyCycle
			}
			if err := registry.Init(t.Context()); !errors.Is(err, want) {
				t.Fatalf("init=%v", err)
			}
			if loads != 0 {
				t.Fatal("invalid topology performed I/O")
			}
		})
	}
}

func TestSharedNotificationRegistrationReportsDuplicateOwners(t *testing.T) {
	for _, name := range []string{"notification.robot", "notification.mail", "notification.sms", "notification", "logging"} {
		t.Run(name, func(t *testing.T) {
			registry := core.NewPluginRegistry()
			if err := registry.Register(nameOnlyPlugin(name)); err != nil {
				t.Fatal(err)
			}
			configuration := defaultconfig.New(func(context.Context) (*config.Config, error) {
				t.Error("unexpected configuration read")
				return nil, nil
			})
			if _, err := registerNotificationLogging(registry, configuration); !errors.Is(err, core.ErrPluginContractViolation) {
				t.Fatalf("duplicate owner error=%v", err)
			}
		})
	}
}
