// 本文件汇总配置的默认值取值：Effective* 系列与凭据取用。
//
// 它们与校验分开：校验回答"这份配置合不合法"，这里回答"没写的字段实际取什么值"。
// 两个问题的答案会各自变化，混在一起时改默认值容易顺手改到校验条件。
package config

import (
	"strings"
	"time"
)

// EffectiveRedisPrefix selects the shared auth/rate-limit/response-cache namespace.
// Empty prefix preserves the original application name byte for byte.
func (c Config) EffectiveRedisPrefix() string {
	if c.Redis.Prefix != "" {
		return c.Redis.Prefix
	}
	return c.App.Name
}

// EffectiveSQLSlowThreshold returns the validated per-instance warning threshold.
func (c LogConfig) EffectiveSQLSlowThreshold() time.Duration {
	if c.SQLSlowThresholdMS == nil {
		return 200 * time.Millisecond
	}
	return time.Duration(*c.SQLSlowThresholdMS) * time.Millisecond
}

// EffectiveDrainWait 返回生效的 LB 传播等待时长。
func (a AppConfig) EffectiveDrainWait() time.Duration {
	if a.DrainWaitSeconds <= 0 {
		return DefaultDrainWait
	}
	return time.Duration(a.DrainWaitSeconds) * time.Second
}

// EffectiveMaxOpenConn 返回生效的最大打开连接数。
func (c DatabaseConfig) EffectiveMaxOpenConn() int {
	if c.MaxOpenConn > 0 {
		return c.MaxOpenConn
	}
	return DefaultDBMaxOpenConn
}

// EffectiveMaxIdleConn 返回生效的最大空闲连接数，且永不超过生效的打开上限。
//
// 回填只看本字段自身，**不看 MaxOpenConn 是否配置**。此前的条件是两者同时未配置
// 才回填，于是"只调大 maxopenconn、不写 maxidleconn"这个常见的扩容动作会得到
// idle=0——而驱动把非正的空闲数解释为不保留任何空闲连接，每个请求新建 TCP +
// 鉴权握手再关闭。它没有任何报错，表现为数据库侧连接数刷屏与响应时延抬升，
// 而运维刚做的恰恰是"扩容"。
//
// 0 一律视为未配置。真要禁用空闲池得写负值，而负值由池参数校验拒绝——
// 本项目不接受零空闲池：让每个请求重建连接，代价与收益完全不成比例。
func (c DatabaseConfig) EffectiveMaxIdleConn() int {
	idle := c.MaxIdleConn
	if idle <= 0 {
		idle = DefaultDBMaxIdleConn
	}
	if open := c.EffectiveMaxOpenConn(); idle > open {
		return open
	}
	return idle
}

// EffectiveMaxLifeSeconds 返回生效的连接最大存活时间（秒）。
func (c DatabaseConfig) EffectiveMaxLifeSeconds() int {
	if c.MaxLifeSeconds > 0 {
		return c.MaxLifeSeconds
	}
	return DefaultDBMaxLifeSeconds
}

// EffectiveCharset 返回生效的连接字符集。
func (c DatabaseConfig) EffectiveCharset() string {
	if s := strings.TrimSpace(c.Charset); s != "" {
		return s
	}
	return DefaultDBCharset
}

// EffectiveCollation 返回生效的连接 collation。
func (c DatabaseConfig) EffectiveCollation() string {
	if s := strings.TrimSpace(c.Collation); s != "" {
		return s
	}
	return DefaultDBCollation
}

// EffectiveMinVersion 返回受支持的最低 MySQL 版本。
func (c DatabaseConfig) EffectiveMinVersion() string {
	if s := strings.TrimSpace(c.MinVersion); s != "" {
		return s
	}
	return DefaultDBMinVersion
}

// EffectiveMaxIdleTimeSeconds 返回生效的空闲连接最长存活时间（秒）。
func (c DatabaseConfig) EffectiveMaxIdleTimeSeconds() int {
	if c.MaxIdleTimeSeconds > 0 {
		return c.MaxIdleTimeSeconds
	}
	return DefaultDBMaxIdleTimeSeconds
}

// EffectivePrepareStmtMaxSize 返回已校验配置的预编译缓存容量，0 使用 128。
func (c DatabaseConfig) EffectivePrepareStmtMaxSize() int {
	if c.PrepareStmtMaxSize > 0 {
		return c.PrepareStmtMaxSize
	}
	return DefaultDBPrepareStmtMaxSize
}

// EffectivePrepareStmtTTLSeconds 返回已校验配置的缓存存活秒数，0 使用 3600。
func (c DatabaseConfig) EffectivePrepareStmtTTLSeconds() int {
	if c.PrepareStmtTTLSeconds > 0 {
		return c.PrepareStmtTTLSeconds
	}
	return DefaultDBPrepareStmtTTLSeconds
}

// RuntimeCredentials 返回服务进程使用的凭据。
// 服务永远只用这一套，不会因为配置了迁移凭据而改变自身连接身份。
func (c DatabaseConfig) RuntimeCredentials() DBCredentials {
	return DBCredentials{Username: c.Username, Password: c.Password}
}

// MigrationCredentials 返回迁移使用的凭据；ok=false 表示未启用，调用方应回退到运行凭据并告警。
func (c DatabaseConfig) MigrationCredentials() (DBCredentials, bool) {
	if strings.TrimSpace(c.MigrateUsername) == "" {
		return DBCredentials{}, false
	}
	return DBCredentials{Username: c.MigrateUsername, Password: c.MigratePassword}, true
}
