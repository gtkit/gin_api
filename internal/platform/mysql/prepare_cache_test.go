package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/ormx"
	"gorm.io/gorm"
)

func TestPrepareStmtOptionsSharedByServerAndMigration(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		size     int
		ttl      int
		wantSize int
		wantTTL  time.Duration
	}{
		{name: "defaults", wantSize: 128, wantTTL: time.Hour},
		{name: "minimum", size: 1, ttl: 1, wantSize: 1, wantTTL: time.Second},
		{name: "maximum", size: 4096, ttl: 86400, wantSize: 4096, wantTTL: 24 * time.Hour},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dbCfg := testDBConfig(3600)
			dbCfg.PrepareStmtMaxSize, dbCfg.PrepareStmtTTLSeconds = tt.size, tt.ttl
			for _, options := range [][]ormx.Option{
				serverOpenOptions(dbCfg, config.DBCredentials{}),
				migrationOpenOptions(dbCfg, config.DBCredentials{}),
			} {
				got := ormx.NewConfig(options...).GORM
				if !got.PrepareStmt || got.PrepareStmtMaxSize != tt.wantSize || got.PrepareStmtTTL != tt.wantTTL {
					t.Fatalf("prepare options = enabled:%v size:%d ttl:%s, want true/%d/%s",
						got.PrepareStmt, got.PrepareStmtMaxSize, got.PrepareStmtTTL, tt.wantSize, tt.wantTTL)
				}
				if !got.SkipDefaultTransaction || !got.TranslateError {
					t.Fatal("cache options changed transaction or error translation semantics")
				}
			}
		})
	}
}

func TestPrepareStmtCacheEvictsAndCloses(t *testing.T) {
	t.Parallel()
	dbCfg := config.DatabaseConfig{PrepareStmtMaxSize: 2}
	cache, events := newObservedStmtCache(t, serverOpenOptions(dbCfg, config.DBCredentials{}))
	queries := []string{"DELETE FROM cache_a WHERE id = ?", "DELETE FROM cache_b WHERE id = ?", "DELETE FROM cache_c WHERE id = ?"}
	for _, query := range queries {
		execCachedStatement(t, cache, query)
		waitForStmtEvent(t, events.prepared, query)
		if got := len(cache.Stmts.Keys()); got > 2 {
			t.Fatalf("resident statements = %d, want <= 2", got)
		}
	}
	if slices.Contains(cache.Stmts.Keys(), queries[0]) {
		t.Fatal("oldest statement remained resident after capacity eviction")
	}
	waitForStmtEvent(t, events.closed, queries[0])
	// 驱动 Close 的通知必须先于测试清理，不能由 sql.DB.Close 假冒淘汰证据。
	execCachedStatement(t, cache, queries[0])
	waitForStmtEvent(t, events.prepared, queries[0])
	if got := len(cache.Stmts.Keys()); got > 2 {
		t.Fatalf("resident statements after reprepare = %d, want <= 2", got)
	}
}

func TestPrepareStmtCacheTTLReprepares(t *testing.T) {
	t.Parallel()
	dbCfg := config.DatabaseConfig{PrepareStmtMaxSize: 2, PrepareStmtTTLSeconds: 1}
	cache, events := newObservedStmtCache(t, migrationOpenOptions(dbCfg, config.DBCredentials{}))
	const query = "DELETE FROM cache_ttl WHERE id = ?"
	execCachedStatement(t, cache, query)
	waitForStmtEvent(t, events.prepared, query)
	execCachedStatement(t, cache, query)
	select {
	case got := <-events.prepared:
		t.Fatalf("unexpired cached statement prepared again: %s", got)
	default:
	}
	// GORM 的清理循环没有停止接口，不能放入 synctest；等实际 Close 通知，最多 3 秒。
	waitForStmtEvent(t, events.closed, query)
	if slices.Contains(cache.Stmts.Keys(), query) {
		t.Fatal("expired statement remained resident")
	}
	execCachedStatement(t, cache, query)
	waitForStmtEvent(t, events.prepared, query)
}

func newObservedStmtCache(t *testing.T, options []ormx.Option) (*gorm.PreparedStmtDB, *stmtCacheEvents) {
	t.Helper()
	events := &stmtCacheEvents{prepared: make(chan string, 16), closed: make(chan string, 16)}
	db := sql.OpenDB(events)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	cfg := ormx.NewConfig(options...).GORM
	cache := gorm.NewPreparedStmtDB(db, cfg.PrepareStmtMaxSize, cfg.PrepareStmtTTL)
	t.Cleanup(func() {
		cache.Close()
		if err := db.Close(); err != nil {
			t.Errorf("close observed database: %v", err)
		}
	})
	return cache, events
}

func execCachedStatement(t *testing.T, cache *gorm.PreparedStmtDB, query string) {
	t.Helper()
	if _, err := cache.ExecContext(t.Context(), query, 1); err != nil {
		t.Fatalf("execute cached statement: %v", err)
	}
}

func waitForStmtEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("statement event = %q, want %q", got, want)
		}
	case <-timer.C:
		t.Fatalf("statement event for %q did not arrive", want)
	case <-t.Context().Done():
		t.Fatalf("waiting for statement event: %v", t.Context().Err())
	}
}

// stmtCacheEvents 只观察 database/sql 实际调用的驱动方法，不做网络连接或 SQL 解析。
type stmtCacheEvents struct {
	prepared chan string
	closed   chan string
}

func (e *stmtCacheEvents) Connect(context.Context) (driver.Conn, error) {
	return &stmtCacheConn{events: e}, nil
}
func (e *stmtCacheEvents) Driver() driver.Driver { return stmtCacheDriver{} }

type stmtCacheDriver struct{}

func (stmtCacheDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("statement cache test requires its connector")
}

type stmtCacheConn struct{ events *stmtCacheEvents }

func (c *stmtCacheConn) Prepare(query string) (driver.Stmt, error) {
	c.events.prepared <- query
	return &observedStmt{query: query, closed: c.events.closed}, nil
}
func (*stmtCacheConn) Close() error { return nil }
func (*stmtCacheConn) Begin() (driver.Tx, error) {
	return nil, errors.New("statement cache test does not use transactions")
}

type observedStmt struct {
	query  string
	closed chan<- string
}

func (s *observedStmt) Close() error {
	s.closed <- s.query
	return nil
}
func (*observedStmt) NumInput() int { return 1 }
func (*observedStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (*observedStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("statement cache test does not query rows")
}
