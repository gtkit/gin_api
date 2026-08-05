package config

import (
	"testing"
	"time"
)

// TestDatabasePoolDefaults 覆盖连接池各项的默认值与生效规则。
//
// 这些函数是"配错了不报错、只是行为不对"的典型：一个写成 0 的
// max_open_conn 若被原样透传给驱动，含义是**无限连接**——数据库会先被打满，
// 而配置文件看上去毫无异常。
func TestDatabasePoolDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  DatabaseConfig
		want map[string]int
	}{
		{
			name: "empty config takes every default",
			cfg:  DatabaseConfig{},
			want: map[string]int{
				"open":     DefaultDBMaxOpenConn,
				"idle":     DefaultDBMaxIdleConn,
				"life":     DefaultDBMaxLifeSeconds,
				"idleTime": DefaultDBMaxIdleTimeSeconds,
			},
		},
		{
			name: "explicit values win",
			cfg: DatabaseConfig{
				MaxOpenConn: 50, MaxIdleConn: 10,
				MaxLifeSeconds: 600, MaxIdleTimeSeconds: 120,
			},
			want: map[string]int{"open": 50, "idle": 10, "life": 600, "idleTime": 120},
		},
		{
			// idle 不能超过 open：超过时驱动会把多出来的连接立刻关掉再建，
			// 表现为连接churn，而配置看起来只是"空闲池大一点"。
			name: "idle is capped at open",
			cfg:  DatabaseConfig{MaxOpenConn: 5, MaxIdleConn: 100},
			want: map[string]int{"open": 5, "idle": 5, "life": DefaultDBMaxLifeSeconds,
				"idleTime": DefaultDBMaxIdleTimeSeconds},
		},
		{
			name: "negative values fall back to defaults",
			cfg: DatabaseConfig{
				MaxOpenConn: -1, MaxLifeSeconds: -1, MaxIdleTimeSeconds: -1,
			},
			want: map[string]int{
				"open":     DefaultDBMaxOpenConn,
				"idle":     DefaultDBMaxIdleConn,
				"life":     DefaultDBMaxLifeSeconds,
				"idleTime": DefaultDBMaxIdleTimeSeconds,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := map[string]int{
				"open":     tt.cfg.EffectiveMaxOpenConn(),
				"idle":     tt.cfg.EffectiveMaxIdleConn(),
				"life":     tt.cfg.EffectiveMaxLifeSeconds(),
				"idleTime": tt.cfg.EffectiveMaxIdleTimeSeconds(),
			}
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("%s = %d, want %d", k, got[k], want)
				}
			}
		})
	}
}

// TestDatabaseStringDefaults 覆盖字符集等字符串项。
//
// 空白字符串也要落到默认值：一个只含空格的 charset 会让 DSN 变成
// `charset=`，MySQL 对此的处理因版本而异，而那种差异只在换环境时才暴露。
func TestDatabaseStringDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                           string
		cfg                            DatabaseConfig
		wantCharset, wantColl, wantVer string
	}{
		{
			name: "empty takes defaults", cfg: DatabaseConfig{},
			wantCharset: DefaultDBCharset, wantColl: DefaultDBCollation, wantVer: DefaultDBMinVersion,
		},
		{
			name:        "blank takes defaults",
			cfg:         DatabaseConfig{Charset: "   ", Collation: "\t", MinVersion: " "},
			wantCharset: DefaultDBCharset, wantColl: DefaultDBCollation, wantVer: DefaultDBMinVersion,
		},
		{
			name:        "explicit values win",
			cfg:         DatabaseConfig{Charset: "utf8", Collation: "utf8_bin", MinVersion: "5.7.0"},
			wantCharset: "utf8", wantColl: "utf8_bin", wantVer: "5.7.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.cfg.EffectiveCharset(); got != tt.wantCharset {
				t.Errorf("charset = %q, want %q", got, tt.wantCharset)
			}
			if got := tt.cfg.EffectiveCollation(); got != tt.wantColl {
				t.Errorf("collation = %q, want %q", got, tt.wantColl)
			}
			if got := tt.cfg.EffectiveMinVersion(); got != tt.wantVer {
				t.Errorf("min version = %q, want %q", got, tt.wantVer)
			}
		})
	}
}

// TestCredentialSeparation 钉住运行凭据与迁移凭据分离。
//
// 服务进程**永远只用运行凭据**：配了迁移凭据也不改变它的连接身份。
// 反过来若服务用了迁移账号，那个账号通常有 DDL 权限——一次 SQL 注入的
// 影响范围会从"读写数据"扩大到"改表结构"。
func TestCredentialSeparation(t *testing.T) {
	t.Parallel()

	t.Run("runtime credentials are unaffected by migration ones", func(t *testing.T) {
		t.Parallel()

		cfg := DatabaseConfig{
			Username: "app", Password: "app-pw",
			MigrateUsername: "ddl", MigratePassword: "ddl-pw",
		}
		got := cfg.RuntimeCredentials()
		if got.Username != "app" || got.Password != "app-pw" {
			t.Fatalf("runtime credentials = %+v, want the app account", got)
		}
	})

	t.Run("migration credentials report whether they are configured", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name     string
			cfg      DatabaseConfig
			wantOK   bool
			wantUser string
		}{
			{name: "configured", wantOK: true, wantUser: "ddl",
				cfg: DatabaseConfig{MigrateUsername: "ddl", MigratePassword: "p"}},
			{name: "absent", wantOK: false,
				cfg: DatabaseConfig{Username: "app"}},
			{name: "blank is absent", wantOK: false,
				cfg: DatabaseConfig{MigrateUsername: "   "}},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				got, ok := tt.cfg.MigrationCredentials()
				if ok != tt.wantOK {
					t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
				}
				if ok && got.Username != tt.wantUser {
					t.Errorf("username = %q, want %q", got.Username, tt.wantUser)
				}
			})
		}
	})
}

// TestEffectiveDrainWait 覆盖关停排空时长。
//
// 它决定"停止接受新请求"与"关闭 HTTP"之间等多久。太短则负载均衡器还没
// 观察到 /ready 的 503 就断了连接，表现为发布期间的零星 502。
func TestEffectiveDrainWait(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		seconds int
		want    time.Duration
	}{
		{name: "explicit value wins", seconds: 15, want: 15 * time.Second},
		{name: "zero takes the default", seconds: 0, want: DefaultDrainWait},
		{name: "negative takes the default", seconds: -3, want: DefaultDrainWait},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := AppConfig{DrainWaitSeconds: tt.seconds}.EffectiveDrainWait()
			if got != tt.want {
				t.Fatalf("EffectiveDrainWait() = %s, want %s", got, tt.want)
			}
		})
	}
}
