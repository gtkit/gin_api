package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// DataIsolateConfig 是共享表租户隔离的进程级配置。
//
// 默认关闭。开启的前置顺序是硬性的：先给受控表加判别列与含该列的唯一约束，
// 再让模型声明该字段，最后才打开开关。顺序反了会在开关打开的瞬间
// fail-closed 掉全部查询——因为那时受控表已经被识别，而身份还没接上。
type DataIsolateConfig struct {
	// Enabled 是总开关。关闭时不安装插件，语句零改写。
	Enabled bool `mapstructure:"enabled"`

	// Column 是判别列名（如 tenant_id），启用时必填、无默认值。
	//
	// 不设默认值是安全决策：列名写错时元数据判定永不命中，于是零张表受控，
	// 隔离**静默消失**而一切看起来正常。这是本能力最坏的失效模式。
	Column string `mapstructure:"column"`

	// IgnoreTables 是豁免表：即便声明了判别列也不受控（全局字典表这类）。
	IgnoreTables []string `mapstructure:"ignore_tables"`

	// Tables 是强制纳入的表。
	Tables []string `mapstructure:"tables"`

	// AllowBypass 决定绕过标记是否真正生效，默认 false。
	//
	// 保持关闭的理由：绕过一旦可用，一次误用就让那条调用脱离隔离，
	// 而它在代码里只表现为多了一行。需要跨租户操作的运维路径更适合走
	// 一个未安装本插件的连接，而不是给整个服务开这道闸。
	AllowBypass bool `mapstructure:"allow_bypass"`
}

// validateDataIsolateConfig 校验租户隔离配置。
//
// 关闭时不校验：母版默认不启用，强制填一组用不到的值只会被随便填。
func validateDataIsolateConfig(cfg DataIsolateConfig) error {
	if !cfg.Enabled {
		return nil
	}

	var errs []error

	if strings.TrimSpace(cfg.Column) == "" {
		errs = append(errs, errors.New(
			"dataisolate.column is required when isolation is enabled: an empty discriminator column "+
				"matches no model, so nothing would be isolated while everything still looks normal"))
	}

	errs = append(errs, validateTableList("dataisolate.ignore_tables", cfg.IgnoreTables)...)
	errs = append(errs, validateTableList("dataisolate.tables", cfg.Tables)...)

	// 同一张表同时出现在两份清单里，配置本身就无法表达作者的意图。
	// 实现侧有确定的赢家（豁免优先），但让作者知道自己写矛盾了更有用——
	// 否则他会以为强制生效了。
	for _, name := range cfg.Tables {
		if slices.Contains(cfg.IgnoreTables, name) {
			errs = append(errs, fmt.Errorf(
				"table %q is listed in both dataisolate.ignore_tables and dataisolate.tables; "+
					"exemption wins, so the entry in tables has no effect", name))
		}
	}

	return errors.Join(errs...)
}

// validateTableList 拒绝清单里的空白项。
//
// 空白项不是"没配"，是一条会去匹配空表名的规则：它永远匹配不到任何表，
// 而作者以为自己豁免了某张表。
func validateTableList(key string, names []string) []error {
	var errs []error
	for i, name := range names {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, fmt.Errorf("%s[%d] is blank: a blank table name matches nothing", key, i))
		}
	}
	return errs
}
