package dao

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// mysqlVendor 是数据库的发行版身份。
//
// 必须先判 vendor 再比版本号：MariaDB 的 `10.6.16` 数值上大于 MySQL 的 `8.0`，
// 但两者 SQL 语义并不等价（默认 collation、sql_mode 默认值、JSON 实现都不同）。
// 只比数字会把 MariaDB 误判成"版本足够高"而放行。
type mysqlVendor uint8

const (
	vendorUnknown mysqlVendor = iota
	vendorOracleMySQL
	vendorPercona
	vendorMariaDB
)

func (v mysqlVendor) String() string {
	switch v {
	case vendorOracleMySQL:
		return "Oracle MySQL"
	case vendorPercona:
		return "Percona Server"
	case vendorMariaDB:
		return "MariaDB"
	default:
		return "unknown"
	}
}

// supported 报告该 vendor 是否走 MySQL 的最低版本线。
//
// Percona Server 与 Oracle MySQL 同源、语义等价，走同一条线。
// MariaDB 与未知 vendor 默认拒绝——放开需要单独的兼容 change，
// 届时必须同时给出它自己的最低版本线与差异说明，而不是在这里加个 case 了事。
func (v mysqlVendor) supported() bool {
	return v == vendorOracleMySQL || v == vendorPercona
}

// serverVersion 是解析后的数据库版本。
type serverVersion struct {
	major, minor, patch int
	vendor              mysqlVendor
	raw                 string
}

func (v serverVersion) String() string {
	return fmt.Sprintf("%s %d.%d.%d (%s)", v.vendor, v.major, v.minor, v.patch, v.raw)
}

// atLeast 报告版本是否不低于 other。
func (v serverVersion) atLeast(other serverVersion) bool {
	if v.major != other.major {
		return v.major > other.major
	}
	if v.minor != other.minor {
		return v.minor > other.minor
	}
	return v.patch >= other.patch
}

// versionPrefixPattern 抓取版本串开头的数值部分。
//
// MySQL 的 VERSION() 形如 `8.0.36`、`8.0.36-0ubuntu0.22.04.1`、
// `10.6.16-MariaDB-1:10.6.16+maria~ubu2004`、`8.0.35-27`（Percona），
// 数值前缀之后的内容各家不同，只取前缀。
var versionPrefixPattern = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?`)

// parseServerVersion 从 VERSION() 与 @@version_comment 解析版本与 vendor。
//
// vendor 判定同时看两个来源：MariaDB 会把标识写进 VERSION() 本身，
// 而 Percona 通常只在 version_comment 里体现。
func parseServerVersion(version, comment string) (serverVersion, error) {
	raw := strings.TrimSpace(version)
	m := versionPrefixPattern.FindStringSubmatch(raw)
	if m == nil {
		return serverVersion{}, fmt.Errorf("cannot parse server version from %q", version)
	}

	major, err := strconv.Atoi(m[1])
	if err != nil {
		return serverVersion{}, fmt.Errorf("cannot parse major version from %q: %w", version, err)
	}
	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return serverVersion{}, fmt.Errorf("cannot parse minor version from %q: %w", version, err)
	}
	patch := 0
	if m[3] != "" {
		if patch, err = strconv.Atoi(m[3]); err != nil {
			return serverVersion{}, fmt.Errorf("cannot parse patch version from %q: %w", version, err)
		}
	}

	return serverVersion{
		major: major, minor: minor, patch: patch,
		vendor: detectVendor(raw, comment),
		raw:    raw,
	}, nil
}

func detectVendor(version, comment string) mysqlVendor {
	haystack := strings.ToLower(version + " " + comment)
	switch {
	case strings.Contains(haystack, "mariadb"):
		return vendorMariaDB
	case strings.Contains(haystack, "percona"):
		return vendorPercona
	case strings.Contains(haystack, "mysql"):
		return vendorOracleMySQL
	default:
		return vendorUnknown
	}
}

// parseMinVersion 解析配置里的最低支持版本。形式已由配置层校验。
func parseMinVersion(v string) (serverVersion, error) {
	m := versionPrefixPattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return serverVersion{}, fmt.Errorf("cannot parse minimum version %q", v)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch := 0
	if m[3] != "" {
		patch, _ = strconv.Atoi(m[3])
	}
	return serverVersion{major: major, minor: minor, patch: patch, raw: v}, nil
}
