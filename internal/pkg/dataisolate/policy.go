package dataisolate

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/shared/tenant"
)

// Operation 标识本次语句属于哪条路径。
type Operation string

// 五条经 clause 构建的路径。裸 SQL 不在其中，理由见包注释。
const (
	OpCreate Operation = "create"
	OpQuery  Operation = "query"
	OpUpdate Operation = "update"
	OpDelete Operation = "delete"
	OpRow    Operation = "row"
)

// Policy 是语句改写的扩展点，行隔离与列隔离经它接入。
//
// 契约：
//   - 只在 SQL 生成之前被调用，因此可以安全地改写 stmt.Clauses 与投影；
//   - 返回 error 即 fail-closed——调用方会中止该语句，不会放行未改写的版本；
//   - 值必须经 clause 的参数占位传递，MUST NOT 拼进 SQL 文本。
//
// 本包只实现租户维度。行与列的可见性规则必然业务特定，母版内置一套猜测的规则
// 会被派生项目当成可用默认值，而它保护的不是它们真实的组织结构。
type Policy interface {
	// Name 用于错误信息，需稳定且可 grep。
	Name() string
	// Apply 改写 stmt。id 是当前租户身份。
	Apply(op Operation, stmt *gorm.Statement, id tenant.ID) error
}

// errNoIdentity 是「命中受控表但上下文没有租户身份」。
//
// 它是 fail-closed 的载体：绝不放行一条未加租户条件的语句。
var errNoIdentity = errors.New("dataisolate: no tenant identity in context")

// errUnknownTable 是「无法判定这张表受不受控」。
//
// 同样 fail-closed。放行才是危险的那个选择：`db.Table("users").Where(…).Find()`
// 会完整绕过隔离，而它不报错、不进日志。
var errUnknownTable = errors.New("dataisolate: cannot tell whether the target table is tenant-scoped")

// hook 返回挂在某条路径上的回调。
//
// 顺序是刻意的：绕过判定 → 受控判定 → 身份判定 → 无条件写保护 → 策略链。
// 每一步都可能中止语句，且中止时给出的错误要指明是哪一步。
func (p *Plugin) hook(op Operation) func(*gorm.DB) {
	return func(db *gorm.DB) {
		// 已有错误时不再介入：语句反正不会执行，继续改写只会让错误信息里
		// 多出一条与真实原因无关的租户错误。
		if db.Error != nil || db.Statement == nil {
			return
		}
		stmt := db.Statement
		ctx := stmt.Context

		// 绕过只在安全闸打开时真正生效。闸门关闭时不绕过，但记住调用方要求过——
		// 否则写了绕过的人只会收到「没有身份」，无从知道是闸门拦下的。
		bypassRequested := tenant.IsBypass(ctx)
		if bypassRequested && p.allowBypass {
			return
		}

		controlled, err := p.controlled(stmt)
		if err != nil {
			_ = db.AddError(fmt.Errorf("%w: table %q has no model metadata; give the statement a Model, "+
				"or keep this call off the handle that carries the isolation plugin", err, stmt.Table))
			return
		}
		if !controlled {
			return
		}

		id, ok := tenant.From(ctx)
		if !ok {
			_ = db.AddError(p.identityError(bypassRequested, stmt.Table))
			return
		}

		// 租户条件不得成为 UPDATE / DELETE 的唯一条件。
		//
		// GORM 自带的「缺少 WHERE 即拒绝」保护只检查 WHERE 子句**是否存在**，
		// 且它在 gorm:update / gorm:delete 回调**内部**执行——晚于这里。
		// 不自行复现的话，一条今天会被拒绝的全表更新会变成
		// 「静默把该租户整张表改掉」，那是本插件引入的新危险。
		if (op == OpUpdate || op == OpDelete) && !hasUserCondition(stmt) {
			_ = db.AddError(fmt.Errorf("%w: dataisolate refuses to let the tenant condition be the only "+
				"predicate of an %s on %q", gorm.ErrMissingWhereClause, op, stmt.Table))
			return
		}

		for _, policy := range p.policies {
			if err := policy.Apply(op, stmt, id); err != nil {
				_ = db.AddError(fmt.Errorf("dataisolate: policy %s: %w", policy.Name(), err))
				return
			}
		}
	}
}

// identityError 组装缺少身份的错误，并按成因给出不同的出路。
//
// 三种成因的下一步完全不同，合并成一句话会让最容易卡住的那种（安全闸）
// 无从排查——调用方明明写了绕过，收到的却是「没有身份」。
func (p *Plugin) identityError(bypassRequested bool, table string) error {
	if bypassRequested {
		return fmt.Errorf("%w: a bypass was requested for %q but the safety gate is closed; "+
			"set the allow-bypass option to permit it, or supply a tenant identity", errNoIdentity, table)
	}
	return fmt.Errorf("%w: %q is tenant-scoped; resolve the tenant from the authenticated "+
		"request into the context, or run this on a handle without the isolation plugin", errNoIdentity, table)
}
