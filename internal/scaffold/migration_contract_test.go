//go:build scaffold

package scaffold

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtkit/migratex/migration"
)

func TestGeneratedMigrationsMatchExecutor(t *testing.T) {
	t.Parallel()
	for _, stamp := range []string{"20261016120000", "20261016125959", "20261231235959"} {
		t.Run(stamp, func(t *testing.T) {
			root := createProjectFixture(t)
			// 项目路径里的方括号是普通文件名，不能被迁移枚举当作 glob。
			destination := filepath.Join(t.TempDir(), "project[dev]")
			if err := os.Rename(root, destination); err != nil {
				t.Fatal(err)
			}
			root = destination
			g, err := NewModuleGenerator(root)
			if err != nil {
				t.Fatal(err)
			}
			now, err := time.Parse("20060102150405", stamp)
			if err != nil {
				t.Fatal(err)
			}
			g.now = func() time.Time { return now }
			for i, name := range []string{"first", "second", "third"} {
				if _, err := g.GenerateWithOptions(name, GenerateOptions{Bootstrap: true, Public: true}); err != nil {
					t.Fatal(err)
				}
				// 下一次生成模拟时钟回拨，仍须严格晚于历史最大版本。
				if i == 1 {
					now = now.Add(-time.Hour)
				}
			}
			dir := filepath.Join(root, "database", "migrations")
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 3 {
				t.Fatalf("files = %v, err = %v", files, err)
			}
			report, err := migration.Lint(dir)
			if err != nil || report.HasErrors() {
				t.Fatalf("executor lint = %+v, err = %v", report, err)
			}
		})
	}
}

func TestMigrationHistoryGuards(t *testing.T) {
	t.Parallel()
	for _, filename := range []string{
		"2026_10_16_create_admins_table.up.sql",
		"2026_10_16_create_admins_table.down.sql",
		"9223372036854775807_other.sql",
		"99991231235959_other.sql",
		"invalid_other.sql",
	} {
		t.Run(filename, func(t *testing.T) {
			root := createProjectFixture(t)
			dir := filepath.Join(root, "database", "migrations")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, filename)
			const content = "-- immutable historical migration\n"
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			g, err := NewModuleGenerator(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := g.GenerateWithOptions("admin", GenerateOptions{Bootstrap: true, Public: true, Force: true}); err == nil {
				t.Fatal("invalid history accepted")
			}
			got, err := os.ReadFile(file)
			if err != nil || string(got) != content {
				t.Fatalf("history changed: %q, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(root, "internal", "models", "admin.go")); !os.IsNotExist(err) {
				t.Fatalf("generation partially wrote files: %v", err)
			}
		})
	}
}
