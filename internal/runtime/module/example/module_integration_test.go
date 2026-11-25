//go:build integration

package example

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/module/example/application"
	"github.com/gtkit/gin-api/internal/platform/config"
	platformmysql "github.com/gtkit/gin-api/internal/platform/mysql"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestIntegrationCRUDMySQL(t *testing.T) {
	if os.Getenv("EXAMPLE_MYSQL_INTEGRATION") != "1" {
		t.Skip("enable the loopback MySQL temporary-table verification")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	host := cfg.Database.Host
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		t.Fatal("example integration requires loopback MySQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool, err := platformmysql.OpenDB(ctx, cfg.Database, cfg.Database.RuntimeCredentials(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	conn, err := pool.SQLDB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	ddl, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "database", "ddl", "create_example_records_table.sql"))
	if err != nil {
		t.Fatal(err)
	}
	const create = "CREATE TABLE `example_records`"
	if !strings.Contains(string(ddl), create) {
		t.Fatal("example schema declaration is missing")
	}
	if _, err := conn.ExecContext(ctx, strings.Replace(string(ddl), create, "CREATE TEMPORARY TABLE `example_records`", 1)); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	count := func(variable string) int {
		t.Helper()
		var key, value string
		if err := conn.QueryRowContext(ctx, "SHOW SESSION STATUS WHERE Variable_name = ?", variable).Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	begin, commit := count("Com_begin"), count("Com_commit")
	manager := dbtx.NewManager(db)
	var baseline application.Record
	if err := manager.Do(ctx, func(txCtx context.Context) error {
		var err error
		baseline, err = service.Create(txCtx, application.CreateRequest{OwnerID: 7, Title: "explicit transaction"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Do(ctx, func(txCtx context.Context) error {
		return service.Delete(txCtx, application.DeleteRequest{OwnerID: 7, ID: baseline.ID})
	}); err != nil {
		t.Fatal(err)
	}
	if count("Com_begin") != begin+2 || count("Com_commit") != commit+2 {
		t.Fatal("explicitly wrapping the single writes did not send one transaction pair per operation")
	}
	begin, commit = count("Com_begin"), count("Com_commit")
	first, err := service.Create(ctx, application.CreateRequest{OwnerID: 7, Title: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(ctx, application.CreateRequest{OwnerID: 7, Title: "two"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Create(ctx, application.CreateRequest{OwnerID: 8, Title: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, application.DeleteRequest{OwnerID: 7, ID: second.ID}); err != nil {
		t.Fatal(err)
	}
	if count("Com_begin") != begin || count("Com_commit") != commit {
		t.Fatal("single writes sent transaction commands")
	}
	persisted, err := service.Get(ctx, application.GetRequest{OwnerID: 7, ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := service.Update(ctx, application.UpdateRequest{OwnerID: 7, ID: first.ID, Title: "one"})
	if err != nil || repeated != persisted {
		t.Fatalf("same title returned %+v with %v, want persisted %+v", repeated, err, persisted)
	}
	if count("Com_begin") != begin+1 || count("Com_commit") != commit+1 {
		t.Fatal("locked update did not use one transaction")
	}
	if err := service.DeleteMany(ctx, application.DeleteManyRequest{OwnerID: 7, IDs: []string{first.ID, other.ID}}); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("mixed batch: %v", err)
	}
	for _, req := range []application.GetRequest{{OwnerID: 7, ID: first.ID}, {OwnerID: 8, ID: other.ID}} {
		if _, err := service.Get(ctx, req); err != nil {
			t.Fatalf("mixed batch partially changed rows: %v", err)
		}
	}
	t.Log("explicitly wrapped writes used one BEGIN/COMMIT pair each; three direct creates and one soft delete used zero pairs; the locked update used one pair")
}
