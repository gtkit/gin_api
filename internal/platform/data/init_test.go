package dao

import (
	"context"
	"testing"

	"github.com/gtkit/ormx/zlogger"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// paramsFilteringLogger 与 gorm.io/gorm/logger 的 ParamsFilter 接口同形状，
// 用于断言 zlogger 的参数化行为；zlogger 不再实现该接口时测试显式失败。
type paramsFilteringLogger interface {
	ParamsFilter(ctx context.Context, sql string, params ...any) (string, []any)
}

func TestGormLoggerOptionsParameterizeQueries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sqlFlag int
	}{
		{name: "sql log disabled still parameterized", sqlFlag: 0},
		{name: "sql log enabled still parameterized", sqlFlag: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{}
			cfg.Log.SQL = tt.sqlFlag

			filtering, ok := zlogger.New(gormLoggerOptions(cfg)...).(paramsFilteringLogger)
			if !ok {
				t.Fatal("zlogger does not implement ParamsFilter; parameterized queries not effective")
			}

			sql, params := filtering.ParamsFilter(context.Background(),
				"SELECT * FROM users WHERE phone = ?", "13800000000")
			if sql != "SELECT * FROM users WHERE phone = ?" {
				t.Fatalf("sql = %q, want parameterized sql unchanged", sql)
			}
			if len(params) != 0 {
				t.Fatalf("params = %v, want bind parameters dropped", params)
			}
		})
	}
}
