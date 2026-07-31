package config

import (
	"fmt"
	"regexp"
)

// minVersionPattern 限定最低支持版本的书写形式为 `major.minor` 或 `major.minor.patch`。
//
// 只校验形式，不在这里做比较：真实版本要连上数据库才知道，比较逻辑属于数据层。
// 但形式必须在启动期就卡住——写错的版本串会让数据层的比较基准本身是错的。
var minVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?$`)

func validateMinVersion(v string) error {
	if !minVersionPattern.MatchString(v) {
		return fmt.Errorf(
			"database.min_version %q is invalid: expected forms are `major.minor` or `major.minor.patch` (e.g. 8.0)", v)
	}
	return nil
}
