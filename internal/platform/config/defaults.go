// 本文件汇总配置的默认值取值：Effective* 系列与凭据取用。
//
// 它们与校验分开：校验回答"这份配置合不合法"，这里回答"没写的字段实际取什么值"。
// 两个问题的答案会各自变化，混在一起时改默认值容易顺手改到校验条件。
package config

import (
	"strings"
	"time"
)

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
func (c DatabaseConfig) EffectiveMaxIdleConn() int {
	idle := c.MaxIdleConn
	if c.MaxOpenConn <= 0 && idle <= 0 {
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
