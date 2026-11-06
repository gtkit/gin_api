//go:build optional

package bootstrap

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/gin-api/internal/optional/outbound"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"
	redisowner "github.com/gtkit/gin-api/internal/platform/redis"
	"gorm.io/gorm"
)

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
