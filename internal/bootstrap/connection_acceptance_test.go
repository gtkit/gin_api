//go:build integration

package bootstrap_test

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/mysql"
	"github.com/gtkit/gin-api/internal/platform/redis"
	gormmysql "gorm.io/driver/mysql"
)

func requireLocalConnection(t *testing.T, host string) {
	t.Helper()
	if host == "localhost" {
		return
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatal("connection acceptance requires a loopback dependency")
	}
}

func TestConnectionAcceptanceMySQL(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	requireLocalConnection(t, cfg.Database.Host)
	for _, migration := range []bool{false, true} {
		name := "server"
		open := mysql.OpenDB
		if migration {
			name, open = "migration", mysql.OpenMigrationDB
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			client, err := open(ctx, cfg.Database, cfg.Database.RuntimeCredentials(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			})
			var row struct{ Version, Comment string }
			if err := client.DB().WithContext(ctx).Raw("SELECT VERSION() AS version, @@version_comment AS comment").Scan(&row).Error; err != nil {
				t.Fatal(err)
			}
			dialect, ok := client.DB().Dialector.(*gormmysql.Dialector)
			if !ok || dialect.ServerVersion != row.Version {
				t.Fatal("driver version differs from the actual server")
			}
			t.Logf("version=%s comment=%s", row.Version, row.Comment)
		})
	}
}

func TestConnectionAcceptanceRedisSmallPools(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(cfg.Redis.Addr)
	if err != nil {
		t.Fatal(err)
	}
	requireLocalConnection(t, host)
	for _, size := range []int{1, 2} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pool := cfg.Redis
			pool.PoolSize, pool.MinIdleConns = size, 0
			client, err := redis.OpenRedis(ctx, pool)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			})
			for _, db := range pool.DBs {
				conn, ok := client.GetClient(db)
				if !ok || conn.Options().PoolSize != size || conn.Options().MinIdleConns > size {
					t.Fatalf("db=%d did not honor the small pool", db)
				}
				if err := conn.Ping(ctx).Err(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
