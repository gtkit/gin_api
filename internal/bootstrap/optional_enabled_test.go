//go:build optional

package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	fileapp "github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/gin-api/internal/optional/outbound"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"
	redisowner "github.com/gtkit/gin-api/internal/platform/redis"
	"gorm.io/gorm"
)

func TestFilePilotRequiresRegisteredPoliciesOnlyWhenEnabled(t *testing.T) {
	cfg := jwtTestConfig(t)
	cfg.Env, cfg.App.Name = "test", "file-pilot"
	cfg.Upload.Enabled = true
	inf := infra{cfg: cfg, resources: &data.Resources{}}
	installMetricsFixture(t, &inf)
	svc := services{file: &fileapp.Service{}}
	auth := func(*gin.Context) {}
	if _, err := buildEngine(inf, svc, auth, nil); err != nil {
		t.Fatalf("disabled pilot required policies: %v", err)
	}
	cfg.Upload.RateLimitEnabled = true
	if _, err := buildEngine(inf, svc, auth, nil); err == nil || !strings.Contains(err.Error(), "files.list.user") || !strings.Contains(err.Error(), "upload.merge.user") {
		t.Fatalf("enabled pilot did not reject missing policies: %v", err)
	}
}

func TestOptionalCapabilityCombinations(t *testing.T) {
	server := miniredis.RunT(t)
	redisCfg := config.RedisConfig{Addr: server.Addr(), DBs: []int{0}}
	client, err := redisowner.OpenRedis(t.Context(), redisCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	resources, err := data.ResourcesFromClients(&lifecycleComponent{db: &gorm.DB{}}, client, true)
	if err != nil {
		t.Fatal(err)
	}
	for mask := range 16 {
		t.Run(fmt.Sprintf("flags-%04b", mask), func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Redis = redisCfg
			cfg.Tasks.Enabled = mask&1 != 0
			if mask&2 != 0 {
				cfg.Outbound.Clients = map[string]outbound.ClientConfig{"fixture": {BaseURL: "https://api.example.test"}}
			}
			if mask&4 != 0 {
				root := t.TempDir()
				cfg.Upload = config.UploadConfig{Enabled: true, Dir: filepath.Join(root, "files"), ChunkDir: filepath.Join(root, "chunks"), MaxFileSize: 1024, AllowedTypes: []string{".txt"}}
			}
			cfg.AuditLog.Enabled = mask&8 != 0
			inf := infra{cfg: cfg, resources: resources, mysqlDB: &gorm.DB{}}
			cleanup := &providerCleanup{}
			t.Cleanup(func() {
				if err := cleanup.rollback(t.Context()); err != nil {
					t.Error(err)
				}
			})
			if err := buildOptionalProviders(&inf, cleanup); err != nil {
				t.Fatal(err)
			}
			if (inf.tasksProducer != nil) != (mask&1 != 0) || (inf.outbound != nil) != (mask&2 != 0) || (inf.uploader != nil) != (mask&4 != 0) || (inf.auditRecorder != nil) != (mask&8 != 0) {
				t.Fatal("optional capability did not follow its switch")
			}
			workers, err := optionalWorkers(&inf)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if cfg.AuditLog.Enabled {
				want = 1
			}
			if len(workers) != want {
				t.Fatalf("workers=%d, want %d without task registrations", len(workers), want)
			}
			if err := cleanup.rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := resources.Rdb(0).Ping(t.Context()).Err(); err != nil {
				t.Fatalf("optional cleanup closed borrowed Redis: %v", err)
			}
		})
	}
}

func TestOptionalFailureRetainsCleanupOwnership(t *testing.T) {
	cfg := &config.Config{}
	cfg.Outbound.Clients = map[string]outbound.ClientConfig{"fixture": {BaseURL: "https://api.example.test"}}
	cfg.AuditLog.Enabled = true
	inf := infra{cfg: cfg}
	cleanup := &providerCleanup{}
	if err := buildOptionalProviders(&inf, cleanup); err == nil {
		t.Fatal("missing audit database accepted")
	}
	if len(cleanup.steps) != 1 {
		t.Fatal("constructed outbound client cleanup was lost")
	}
	if err := cleanup.rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(cleanup.steps) != 0 {
		t.Fatal("cleanup ownership retained after rollback")
	}
}

func TestUploadRequiresRedisAndReadinessRecovers(t *testing.T) {
	server := miniredis.RunT(t)
	cfg := jwtTestConfig(t)
	cfg.Auth.Mode = config.AuthModeExternal
	cfg.App.Name = "upload-readiness"
	cfg.Redis = config.RedisConfig{Addr: server.Addr(), DBs: []int{0, 2}}
	cfg.Upload.Enabled = true
	cfg.Upload.RedisDB = 2

	server.SetError("ERR redis maintenance")
	openRedis := defaultRuntimeFactories().redis
	if owner, err := openRedis(t.Context(), cfg); err == nil || owner != nil {
		t.Fatalf("required Redis failure: owner exists=%t, err=%v", owner != nil, err)
	}
	cfg.Upload.Enabled = false
	optional, err := openRedis(t.Context(), cfg)
	if err != nil || optional == nil {
		t.Fatalf("optional Redis failure blocked startup: %v", err)
	}
	if err := optional.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	cfg.Upload.Enabled = true
	server.SetError("")
	owner, err := openRedis(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		if err := owner.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	db := &gorm.DB{}
	resources, err := data.ResourcesFromClients(&lifecycleComponent{db: db}, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	inf := infra{cfg: cfg, resources: resources, mysqlDB: db}
	installMetricsFixture(t, &inf)
	engine, err := buildEngine(inf, services{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ready := func() int {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
		return w.Code
	}
	waitReady := func(want int) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			got := ready()
			if got == want {
				return
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("readiness status=%d, want %d", got, want)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	waitReady(http.StatusOK)
	server.SetError("ERR redis maintenance")
	waitReady(http.StatusServiceUnavailable)
	server.SetError("")
	waitReady(http.StatusOK)

	cfg.Upload.RedisDB = 7
	cleanup := &providerCleanup{}
	if err := buildUploader(&inf, cleanup); err == nil || !strings.Contains(err.Error(), "upload.redis_db 7") {
		t.Fatalf("missing configured client: %v", err)
	}
	if inf.uploader != nil || len(cleanup.steps) != 0 {
		t.Fatal("an uploader was started without its Redis client")
	}
}
