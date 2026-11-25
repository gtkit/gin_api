//go:build integration

package bootstrap_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gin-api/internal/platform/mysql"
	"github.com/gtkit/gin-api/internal/platform/redis"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	gormmysql "gorm.io/driver/mysql"
	gormlogger "gorm.io/gorm/logger"
)

func TestConnectionAcceptanceMySQLTLS(t *testing.T) {
	if os.Getenv("MYSQL_ITEST_TLS") != "1" {
		t.Skip("set MYSQL_ITEST_TLS=1 to exercise the local MySQL TLS handshake")
	}
	ca := os.Getenv("MYSQL_ITEST_TLS_CA_FILE")
	local, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	requireLocalConnection(t, local.Database.Host)
	for _, tt := range []struct {
		name string
		open func(context.Context, config.DatabaseConfig, config.DBCredentials, gormlogger.Interface) (mysql.Client, error)
	}{
		{"server", mysql.OpenDB},
		{"migration", mysql.OpenMigrationDB},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cfg := local.Database
			cfg.TLS = config.ClientTLSConfig{Enabled: true, ServerName: "unmatched.invalid", CAFile: ca}
			client, err := tt.open(ctx, cfg, cfg.RuntimeCredentials(), nil)
			if client != nil {
				_ = client.Close()
				t.Fatal("server with unmatched certificate was accepted")
			}
			if _, ok := errors.AsType[*tls.CertificateVerificationError](err); !ok {
				t.Fatalf("expected TLS certificate verification failure, got %v", err)
			}
			cfg.TLS.ServerName = os.Getenv("MYSQL_ITEST_TLS_SERVER_NAME")
			if cfg.TLS.ServerName == "" {
				t.Logf("certificate failed verification; positive TLS check needs a matching certificate SAN: %v", err)
				return
			}
			if _, ok := errors.AsType[x509.HostnameError](err); !ok {
				t.Fatalf("expected certificate hostname verification failure, got %v", err)
			}
			client, err = tt.open(ctx, cfg, cfg.RuntimeCredentials(), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			})
			client.SQLDB().SetMaxIdleConns(0)
			for range 2 {
				var status, cipher string
				if err := client.SQLDB().QueryRowContext(ctx, "SHOW SESSION STATUS LIKE 'Ssl_cipher'").Scan(&status, &cipher); err != nil || cipher == "" {
					t.Fatalf("session TLS cipher=%q err=%v", cipher, err)
				}
			}
		})
	}
}

func requireLocalConnection(t testing.TB, host string) {
	t.Helper()
	if host == "localhost" {
		return
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatal("connection acceptance requires a loopback dependency")
	}
}

func BenchmarkDataTracingConnections(b *testing.B) {
	cfg, err := config.Load()
	if err != nil {
		b.Fatal(err)
	}
	requireLocalConnection(b, cfg.Database.Host)
	host, _, err := net.SplitHostPort(cfg.Redis.Addr)
	if err != nil {
		b.Fatal(err)
	}
	requireLocalConnection(b, host)
	for _, enabled := range []bool{false, true} {
		b.Run(strconv.FormatBool(enabled), func(b *testing.B) {
			db, err := mysql.OpenDB(b.Context(), cfg.Database, cfg.Database.RuntimeCredentials(), nil)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := db.Close(); err != nil {
					b.Error(err)
				}
			})
			redisDB, err := redis.OpenRedis(b.Context(), cfg.Redis)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := redisDB.Close(); err != nil {
					b.Error(err)
				}
			})
			if enabled {
				previous := otel.GetTracerProvider()
				provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
				otel.SetTracerProvider(provider)
				b.Cleanup(func() {
					otel.SetTracerProvider(previous)
					if err := provider.Shutdown(context.Background()); err != nil {
						b.Error(err)
					}
				})
				if err := data.InstrumentDatabaseTracing(db.DB(), cfg.Database.Name); err != nil {
					b.Fatal(err)
				}
				if err := data.InstrumentRedisClient(redisDB.DefaultClient()); err != nil {
					b.Fatal(err)
				}
			}
			request := func() {
				var value int
				if err := db.DB().WithContext(b.Context()).Raw("SELECT 1").Scan(&value).Error; err != nil {
					b.Fatal(err)
				}
				if err := redisDB.DefaultClient().Ping(b.Context()).Err(); err != nil {
					b.Fatal(err)
				}
			}
			request() // Warm the connection and statement cache before timing.
			b.ReportAllocs()
			for b.Loop() {
				request()
			}
		})
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
			checkStrictMySQLSessions(t, ctx, client.SQLDB())
		})
	}
}

func checkStrictMySQLSessions(t *testing.T, ctx context.Context, pool *sql.DB) {
	t.Helper()
	// 用固定契约表达式验证数据库行为，实际连接参数由 mysql 包的测试验证。
	const enableStrictMode = "SET SESSION sql_mode = CONCAT_WS(',',NULLIF(@@SESSION.sql_mode,''),'STRICT_TRANS_TABLES')"
	pool.SetMaxOpenConns(2)
	pool.SetMaxIdleConns(0)
	borrow := func() (*sql.Conn, int64) {
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := conn.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
				t.Error(err)
			}
		})
		var id int64
		if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(mysqlSessionModes(t, ctx, conn), "STRICT_TRANS_TABLES") {
			t.Fatal("a new physical connection did not enable strict SQL mode")
		}
		return conn, id
	}
	first, firstID := borrow()
	second, secondID := borrow()
	if firstID == secondID {
		t.Fatal("the pool did not provide distinct physical connections")
	}
	if _, err := first.ExecContext(ctx, "CREATE TEMPORARY TABLE strict_session_probe (text_value VARCHAR(1), number_value INT, required_value INT NOT NULL) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ initial, setup string }{
		{"", "SET SESSION sql_mode = ''"},
		{"NO_ENGINE_SUBSTITUTION,ANSI_QUOTES", "SET SESSION sql_mode = 'NO_ENGINE_SUBSTITUTION,ANSI_QUOTES'"},
		{"STRICT_TRANS_TABLES,ONLY_FULL_GROUP_BY", "SET SESSION sql_mode = 'STRICT_TRANS_TABLES,ONLY_FULL_GROUP_BY'"},
		{"STRICT_ALL_TABLES,NO_BACKSLASH_ESCAPES", "SET SESSION sql_mode = 'STRICT_ALL_TABLES,NO_BACKSLASH_ESCAPES'"},
	} {
		t.Run("initial="+tc.initial, func(t *testing.T) {
			if _, err := first.ExecContext(ctx, tc.setup); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := first.ExecContext(ctx, enableStrictMode); err != nil {
					t.Fatal(err)
				}
			}
			modes := mysqlSessionModes(t, ctx, first)
			for mode := range strings.SplitSeq(tc.initial+",STRICT_TRANS_TABLES", ",") {
				if mode != "" && !slices.Contains(modes, mode) {
					t.Fatalf("SQL mode %q was lost", mode)
				}
			}
			for _, tc := range []struct {
				query string
				code  uint16
			}{
				{"INSERT INTO strict_session_probe VALUES ('ab',1,1)", 1406},
				{"INSERT INTO strict_session_probe VALUES ('a','invalid',1)", 1366},
				{"INSERT INTO strict_session_probe (text_value,number_value) VALUES ('a',1)", 1364},
			} {
				_, err := first.ExecContext(ctx, tc.query)
				if failure, ok := errors.AsType[*mysqldriver.MySQLError](err); !ok || failure.Number != tc.code {
					t.Fatalf("invalid write must return MySQL error %d, got %v", tc.code, err)
				}
			}
			var count int
			if err := first.QueryRowContext(ctx, "SELECT COUNT(*) FROM strict_session_probe").Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid writes persisted rows: count=%d, error=%v", count, err)
			}
			if _, err := first.ExecContext(ctx, "INSERT INTO strict_session_probe VALUES ('a',1,1)"); err != nil {
				t.Fatal(err)
			}
			if _, err := first.ExecContext(ctx, "DELETE FROM strict_session_probe"); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, conn := range []*sql.Conn{first, second} {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	_, replacementID := borrow()
	if replacementID == firstID || replacementID == secondID {
		t.Fatal("the reconnect check reused a physical connection")
	}
}

func mysqlSessionModes(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	var modes string
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&modes); err != nil {
		t.Fatal(err)
	}
	return strings.Split(modes, ",")
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
