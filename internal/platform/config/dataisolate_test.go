package config

import (
	"strings"
	"testing"
)

// TestValidateDataIsolateConfig 覆盖启用态的全部校验分支。
//
// 关闭态单列一条：母版默认不启用，而"关闭时不该因为没填而失败"正是
// 使用者第一次跑起来的路径——它坏了没人能启动。
func TestValidateDataIsolateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     DataIsolateConfig
		wantSub string // 空表示应当通过
	}{
		{
			name: "关闭时不校验",
			cfg:  DataIsolateConfig{Enabled: false},
		},
		{
			name: "关闭时即便其它字段为空也通过",
			cfg:  DataIsolateConfig{Enabled: false, Column: "", IgnoreTables: []string{""}},
		},
		{
			name: "启用且配置完整",
			cfg: DataIsolateConfig{
				Enabled:      true,
				Column:       "tenant_id",
				IgnoreTables: []string{"sys_dict"},
				Tables:       []string{"legacy_rows"},
			},
		},
		{
			name:    "启用但列名为空",
			cfg:     DataIsolateConfig{Enabled: true},
			wantSub: "dataisolate.column is required",
		},
		{
			name:    "启用但列名只有空白",
			cfg:     DataIsolateConfig{Enabled: true, Column: "   "},
			wantSub: "dataisolate.column is required",
		},
		{
			name: "豁免清单有空白项",
			cfg: DataIsolateConfig{
				Enabled: true, Column: "tenant_id", IgnoreTables: []string{"ok", "  "},
			},
			wantSub: "dataisolate.ignore_tables[1] is blank",
		},
		{
			name: "强制清单有空白项",
			cfg: DataIsolateConfig{
				Enabled: true, Column: "tenant_id", Tables: []string{""},
			},
			wantSub: "dataisolate.tables[0] is blank",
		},
		{
			name: "同一张表同时出现在两份清单里",
			cfg: DataIsolateConfig{
				Enabled: true, Column: "tenant_id",
				IgnoreTables: []string{"both"}, Tables: []string{"both"},
			},
			wantSub: "listed in both",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateDataIsolateConfig(tc.cfg)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("validateDataIsolateConfig() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateDataIsolateConfig() = nil, want an error containing %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("validateDataIsolateConfig() = %q, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}
