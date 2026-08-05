package config

import (
	"strings"
	"testing"
	"time"
)

// TestPortRangeIsEnforced 钉住端口范围校验。
//
// **这是本次补上的真实漏洞**：此前只校验能否 strconv.Atoi，于是 99999 与 -1
// 都能通过启动校验，直到 net.Listen 或建连时才失败——那时的错误来自系统调用，
// 与"配置写错了"这个真实原因隔着好几层。
func TestPortRangeIsEnforced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		port    string
		wantErr bool
	}{
		{name: "typical port", port: "8080"},
		{name: "lower bound", port: "1"},
		{name: "upper bound", port: "65535"},

		{name: "zero rejected", port: "0", wantErr: true},
		{name: "negative rejected", port: "-1", wantErr: true},
		{name: "above upper bound rejected", port: "65536", wantErr: true},
		{name: "far above rejected", port: "99999", wantErr: true},
		{name: "non numeric rejected", port: "http", wantErr: true},
		{name: "decimal rejected", port: "80.5", wantErr: true},

		// 空串跳过：缺失由 mustNonEmpty 单独报，避免同一字段触发两条错误。
		{name: "empty is skipped", port: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := mustPortRange("x.port", tt.port)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("mustPortRange(%q) = %v, wantErr = %v", tt.port, err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "x.port") {
				t.Errorf("error %q does not name the field", err)
			}
		})
	}
}

// TestAppAndDatabasePortsAreRangeChecked 钉住漏洞在**真实校验路径**上被堵住。
//
// 单测 helper 还不够：它证明 helper 本身是对的，不证明 Validate 真的调用了它。
func TestAppAndDatabasePortsAreRangeChecked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mut  func(*Config)
	}{
		{name: "app port too large", mut: func(c *Config) { c.App.Port = "99999" }},
		{name: "app port zero", mut: func(c *Config) { c.App.Port = "0" }},
		{name: "database port too large", mut: func(c *Config) { c.Database.Port = "70000" }},
		{name: "database port negative", mut: func(c *Config) { c.Database.Port = "-1" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfigForTest()
			tt.mut(cfg)

			if err := cfg.Validate(); err == nil {
				t.Fatal("an out-of-range port passed validation: it would only fail later " +
					"at listen/dial time, where the error comes from the network stack and " +
					"no longer points at the config file")
			}
		})
	}
}

// TestRedisAddrIsRangeChecked 钉住 host:port 形式同样查范围。
func TestRedisAddrIsRangeChecked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{name: "typical", addr: "127.0.0.1:6379"},
		{name: "hostname", addr: "redis.internal:6379"},
		{name: "ipv6", addr: "[::1]:6379"},

		{name: "port out of range", addr: "127.0.0.1:99999", wantErr: true},
		{name: "port zero", addr: "127.0.0.1:0", wantErr: true},
		{name: "no port", addr: "127.0.0.1", wantErr: true},
		{name: "empty host", addr: ":6379", wantErr: true},
		{name: "non numeric port", addr: "127.0.0.1:redis", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := mustHostPort("redis.addr", tt.addr)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("mustHostPort(%q) = %v, wantErr = %v", tt.addr, err, tt.wantErr)
			}
		})
	}
}

// TestNumericHelpersAcceptEveryRelevantType 钉住泛型 helper 覆盖三种类型。
//
// `~int64` 天然涵盖 time.Duration（它就是 int64 的别名），因此一个 helper
// 同时处理 int、int64 与 Duration，不必按类型各写一份。
func TestNumericHelpersAcceptEveryRelevantType(t *testing.T) {
	t.Parallel()

	t.Run("positive", func(t *testing.T) {
		t.Parallel()

		if err := mustPositive("a", 1); err != nil {
			t.Errorf("int: %v", err)
		}
		if err := mustPositive("b", int64(1)); err != nil {
			t.Errorf("int64: %v", err)
		}
		if err := mustPositive("c", time.Second); err != nil {
			t.Errorf("duration: %v", err)
		}
		if err := mustPositive("d", 0); err == nil {
			t.Error("zero passed mustPositive")
		}
		if err := mustPositive("e", -time.Second); err == nil {
			t.Error("a negative duration passed mustPositive")
		}
	})

	t.Run("non-negative", func(t *testing.T) {
		t.Parallel()

		if err := mustNonNegative("a", 0); err != nil {
			t.Errorf("zero must be allowed: %v", err)
		}
		if err := mustNonNegative("b", time.Duration(0)); err != nil {
			t.Errorf("zero duration must be allowed: %v", err)
		}
		if err := mustNonNegative("c", -1); err == nil {
			t.Error("a negative value passed mustNonNegative")
		}
	})
}

// TestMustNonEmptyRejectsBlank 钉住只含空白的值也算缺失。
//
// 一个只有空格的 database.name 会拼进 DSN 变成 `dbname= `，
// 而报错来自数据库握手——同样看不出是配置写错了。
func TestMustNonEmptyRejectsBlank(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "real value", value: "app"},
		{name: "empty", value: "", wantErr: true},
		{name: "spaces", value: "   ", wantErr: true},
		{name: "tab", value: "\t", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := mustNonEmpty("x", tt.value)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("mustNonEmpty(%q) = %v, wantErr = %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

// TestMustTimezone 覆盖时区校验。
func TestMustTimezone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tz      string
		wantErr bool
	}{
		{name: "shanghai", tz: "Asia/Shanghai"},
		{name: "utc", tz: "UTC"},
		{name: "empty is allowed", tz: ""},
		{name: "nonsense rejected", tz: "Mars/Olympus", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := mustTimezone("application.timezone", tt.tz)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("mustTimezone(%q) = %v, wantErr = %v", tt.tz, err, tt.wantErr)
			}
		})
	}
}
