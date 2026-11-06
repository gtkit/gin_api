package auditlog

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm/schema"
)

func TestOperationAuditSchemaMatchesMigration(t *testing.T) {
	t.Parallel()
	s, err := schema.Parse(&Record{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	const indexName = "idx_operation_audit_logs_user_occurred"
	var columns []string
	for _, index := range s.ParseIndexes() {
		if index.Name == indexName {
			for _, field := range index.Fields {
				columns = append(columns, field.DBName)
			}
		}
	}
	if !slices.Equal(columns, []string{"user_id", "occurred_at"}) {
		t.Errorf("audit query index columns=%v, want [user_id occurred_at]", columns)
	}
	if got := mysql.New(mysql.Config{}).DataTypeOf(s.LookUpField("Status")); got != "int" {
		t.Errorf("audit status SQL type=%s, want int", got)
	}
	for _, path := range []string{
		filepath.Join("migrations", "20260819000001_create_operation_audit_logs_table.sql"),
		filepath.Join("ddl", "create_operation_audit_logs_table.sql"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "`"+indexName+"` (`user_id`, `occurred_at`)") ||
			!strings.Contains(string(data), "`status` int NOT NULL") {
			t.Errorf("%s differs from the audit model schema contract", path)
		}
	}
}
