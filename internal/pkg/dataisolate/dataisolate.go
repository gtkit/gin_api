// Package dataisolate 以 GORM 插件的形式为共享表提供租户数据隔离：
// 在 SQL 生成之前为受控表的读写注入判别列条件，并为写入行盖章。
//
// 业务代码不感知它。仓储照常经各自的 mdbCtx(ctx) 访问数据库，条件由回调注入。
//
// 为什么是回调而不是改写 SQL 文本：GORM 在 SQL 生成**之前**暴露 Statement，
// 条件经 clause 注入，值只走参数占位。移植来源（aifei-go 的 plugins/dataisolate）
// 不得不引入 SQL 解析库并处理 `?` ↔ `$N` 占位符桥接，是因为它的数据层只交出
// 成品 SQL 文本——那一整块复杂度在 GORM 上不存在。
//
// 覆盖范围与它的边界：
//
//	覆盖  Create / Query / Update / Delete / Row 五条经 clause 构建的路径。
//	不覆盖 裸 SQL（db.Raw / db.Exec）——那里 Statement 里是调用方给的 SQL 文本，
//	      没有可注入的 clause 结构。该边界由架构传感器挡住（数据访问层禁止裸 SQL），
//	      而不是靠"检查 SQL 文本里有没有判别列"假装覆盖：文本匹配把正确性押在
//	      SQL 的书写形态上，与本包拒绝依赖括号启发式的理由相同。
//
// 装配面也是一条边界：本插件只装在**服务运行时**的数据库句柄上，不装在迁移
// 运行时。`gtkit/migratex` 读版本表时用 `.Table(...)` 不带 Model，那类语句没有
// 模型元数据，会撞上本包的 fail-closed。既有的 otelgorm 插件同样只装在服务运行时。
package dataisolate

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// pluginName 是 GORM 插件注册名。
//
// 带仓库前缀是为了与第三方插件区分：GORM 的插件表按名去重，撞名会让后装的
// 那个静默替换先装的。
const pluginName = "gin-api:tenant-isolation"

// callbackPrefix 是本插件全部回调的注册前缀。
//
// 与 otelgorm 的 `otel:` 前缀同一思路：回调也按名注册，同名会互相覆盖。
// 已核实 otelgorm 使用 `otel:` 前缀，两者不冲突。
const callbackPrefix = "tenant:"

// Options 是插件的装配参数。
type Options struct {
	// Column 是判别列名（如 "tenant_id"），必填、无默认值。
	//
	// 不设默认值是安全决策：列名写错时 LookUpField 永不命中，于是零张表受控，
	// 隔离**静默消失**而一切看起来正常。这是本能力最坏的失效模式，
	// 因此列名必须由装配方显式写下，并由 Models 校验它确实存在。
	Column string

	// Models 是本库的受管模型，用于装配期校验，必填。
	//
	// 它承担两件在运行期无法补救的校验：判别列至少存在于一个模型上（防列名写错），
	// 以及豁免/强制清单里的表名确实存在（防清单比现实活得久）。
	// 运行期的受控判定仍以语句自身的模型元数据为准，见 controlled。
	Models []any

	// IgnoreTables 是豁免表：即便声明了判别列也不受控（全局字典表这类）。
	IgnoreTables []string

	// ForceTables 是强制纳入的表。
	ForceTables []string

	// AllowBypass 是逃逸口的安全闸。默认 false——绕过标记不生效。
	AllowBypass bool
}

// Plugin 实现 gorm.Plugin。
//
// 字段在 New 之后只读，因此并发安全；本包不持有任何包级可变状态
// （包级默认 + 设置函数那种装配形态在本仓过不了 check-package-vars）。
type Plugin struct {
	// opts 保留装配参数，供 Initialize 期的校验使用（那里才拿得到命名策略）。
	opts Options

	column      string
	ignore      map[string]bool
	force       map[string]bool
	allowBypass bool

	// knownControlled 记录受管模型的表名 → 是否声明了判别列。
	//
	// 只在语句缺少模型元数据时作为回退依据；有元数据时以元数据为准，
	// 这样一个**未注册**但声明了判别列的模型仍然受保护（失效方向偏安全）。
	knownControlled map[string]bool

	// policies 是策略链，租户策略恒为第一条。行/列隔离经此扩展。
	policies []Policy
}

// New 构造插件。
//
// 这里只做不依赖数据库的检查。依赖命名策略的那部分校验在 Initialize——
// 表名由 db.NamingStrategy 推导，用别的策略算出来的表名会与运行期对不上，
// 于是豁免清单与兜底集合都是错的，而这种错不会有任何症状。
func New(opts Options) (*Plugin, error) {
	if opts.Column == "" {
		return nil, errors.New("dataisolate: column is required: an empty discriminator column silently controls no table at all")
	}
	if len(opts.Models) == 0 {
		return nil, errors.New("dataisolate: models are required: without them the column name and the exemption lists cannot be validated")
	}

	return &Plugin{
		opts:     opts,
		column:   opts.Column,
		policies: []Policy{tenantPolicy{column: opts.Column}},
	}, nil
}

// validate 完成依赖命名策略的装配期校验。
//
// 这两类错误在运行期都不会报错，只会让隔离静默地少覆盖一部分——
// 那时已经没有信号可看，因此必须在装配期就失败。
func (p *Plugin) validate(namer schema.Namer) error {
	known, err := describeModels(p.opts.Models, p.column, namer)
	if err != nil {
		return err
	}
	if !slices.Contains(slices.Collect(maps.Values(known)), true) {
		return fmt.Errorf(
			"dataisolate: no managed model declares column %q, so nothing would be isolated; check the column name against the models",
			p.column)
	}

	ignore, err := indexTables(p.opts.IgnoreTables, known, "ignore_tables")
	if err != nil {
		return err
	}
	force, err := indexTables(p.opts.ForceTables, known, "tables")
	if err != nil {
		return err
	}

	p.ignore = ignore
	p.force = force
	p.allowBypass = p.opts.AllowBypass
	p.knownControlled = known
	return nil
}

// Register 追加一条策略，供行隔离与列隔离扩展。
//
// 租户策略恒为第一条且不可移除：后续策略在它之后运行，因此看到的已是
// 限定在本租户内的语句。
func (p *Plugin) Register(policy Policy) {
	p.policies = append(p.policies, policy)
}

// Name 实现 gorm.Plugin。
func (p *Plugin) Name() string { return pluginName }

// Initialize 实现 gorm.Plugin，注册五条经 clause 构建的路径。
//
// 刻意**不**注册 Raw：那条路径没有可注入的 clause 结构（见包注释）。
// 在那里 fail-closed 也不可行——它会连带打断基础设施自身的裸 SQL
// （如启动期的连接校验），而那些语句压根不碰业务表。
func (p *Plugin) Initialize(db *gorm.DB) error {
	if err := p.validate(db.NamingStrategy); err != nil {
		return err
	}

	cb := db.Callback()

	// 存 Register 方法值而不是 *callback：processor.Before 返回的是 GORM 的
	// **未导出**类型，无法在本包里命名。方法值的类型是导出可写的。
	regs := []struct {
		name     string
		op       Operation
		register func(string, func(*gorm.DB)) error
	}{
		{"create", OpCreate, cb.Create().Before("gorm:create").Register},
		{"query", OpQuery, cb.Query().Before("gorm:query").Register},
		{"update", OpUpdate, cb.Update().Before("gorm:update").Register},
		{"delete", OpDelete, cb.Delete().Before("gorm:delete").Register},
		{"row", OpRow, cb.Row().Before("gorm:row").Register},
	}

	for _, r := range regs {
		if err := r.register(callbackPrefix+r.name, p.hook(r.op)); err != nil {
			return fmt.Errorf("dataisolate: register %s callback: %w", r.name, err)
		}
	}
	return nil
}

// describeModels 解析受管模型，返回 表名 → 是否声明判别列。
//
// namer 必须是 db 自己的命名策略：表名与列名的推导规则必须与实际执行时一致，
// 否则装配期算出的是另一套表名——校验照样通过，而运行期一条都对不上。
// cacheStore 用独立实例，只为校验，不污染 db 自己的解析缓存。
func describeModels(models []any, column string, namer schema.Namer) (map[string]bool, error) {
	cache := &sync.Map{}
	known := make(map[string]bool, len(models))

	for _, m := range models {
		s, err := schema.Parse(m, cache, namer)
		if err != nil {
			return nil, fmt.Errorf("dataisolate: parse model %T: %w", m, err)
		}
		known[s.Table] = s.LookUpField(column) != nil
	}
	return known, nil
}

// indexTables 把配置里的表名清单转成集合，并拒绝其中不存在的表名。
//
// 解析不到即失败，与「豁免清单不得比现实活得久」同一条纪律：一条指向已不存在
// 的表的豁免，读起来像"这张表被有意放行"，实际是没人维护的残留——
// 而下一次真正需要豁免的表漏配时，没有任何信号能区分这两种情况。
func indexTables(names []string, known map[string]bool, key string) (map[string]bool, error) {
	if len(names) == 0 {
		return nil, nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		if _, ok := known[n]; !ok {
			return nil, fmt.Errorf(
				"dataisolate: %s lists table %q, which no managed model declares; remove the entry or register the model",
				key, n)
		}
		set[n] = true
	}
	return set, nil
}
