package dataisolate

import (
	"context"
	"fmt"
	"reflect"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"

	"github.com/gtkit/gin-api/internal/shared/tenant"
)

// whereClause 是 GORM 里 WHERE 子句在 Statement.Clauses 中的键。
const whereClause = "WHERE"

// tenantPolicy 是租户维度的策略：读写路径注入判别列条件，写入路径盖章。
type tenantPolicy struct {
	column string
}

func (tenantPolicy) Name() string { return "tenant" }

// Apply 按操作分流。
//
// INSERT 走行盖章而不是条件注入：那里没有 WHERE 可注入，要保证的是写进去的行
// 带着正确的租户。UPDATE / DELETE 只注入 WHERE、不碰 SET——越权写的入口是
// 「改到别人的行」，不是「改错列」。
func (t tenantPolicy) Apply(op Operation, stmt *gorm.Statement, id tenant.ID) error {
	if op == OpCreate {
		if err := t.rejectForeignTenant(stmt, id); err != nil {
			return err
		}
		stmt.SetColumn(t.column, string(id), true)
		return nil
	}
	injectCondition(stmt, t.column, string(id))
	return nil
}

// rejectForeignTenant 拒绝「调用方显式写了另一个租户」的插入。
//
// 三种处置里只有报错是对的：
//
//	静默覆盖  —— 能防住越权，但把调用方明确写下的值悄悄丢掉。那要么是 bug，
//	             要么是越权尝试，两者都不该无声无息。
//	原样放行  —— 任何能构造行的代码都能往别的租户写数据，隔离形同虚设。
//	报错      —— 冲突暴露在写入点，且相同值不受影响。
//
// 种子数据、跨租户导入这类确实需要写别的租户的场景走绕过口，
// 那条路径在策略链之前就短路了，不会走到这里。
func (t tenantPolicy) rejectForeignTenant(stmt *gorm.Statement, id tenant.ID) error {
	if stmt.Schema == nil {
		return nil
	}
	field := stmt.Schema.LookUpField(t.column)
	if field == nil {
		return nil
	}

	rv := stmt.ReflectValue
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := range rv.Len() {
			if err := conflictOf(stmt.Context, field, rv.Index(i), id, i); err != nil {
				return err
			}
		}
	case reflect.Struct:
		return conflictOf(stmt.Context, field, rv, id, -1)
	}
	return nil
}

// conflictOf 报告单行是否携带了与当前租户不同的租户值。index 为负表示非批量。
func conflictOf(ctx context.Context, field *schema.Field, row reflect.Value, id tenant.ID, index int) error {
	value, zero := field.ValueOf(ctx, row)
	if zero {
		return nil
	}
	// 用 Sprint 比较而不是断言成 string：判别列在某些库里是整型，
	// 而这里要判的是"是不是同一个租户"，不是"底层类型是什么"。
	if got := fmt.Sprint(value); got != string(id) {
		if index >= 0 {
			return fmt.Errorf("row %d already carries tenant %q while the context tenant is %q; "+
				"use the bypass escape hatch if writing into another tenant is intended", index, got, id)
		}
		return fmt.Errorf("the row already carries tenant %q while the context tenant is %q; "+
			"use the bypass escape hatch if writing into another tenant is intended", got, id)
	}
	return nil
}

// group 把一组条件整体括起来。
//
// 这是本包正确性的关键。GORM 的 buildExprs 默认以 AND 连接表达式，但**单元素
// OrConditions 会把连接符改成 OR**，而 Where.MergeClause 把新表达式追加到尾部。
// 于是把租户条件直接追加到 `Where(A).Or(B)` 之后会得到
//
//	WHERE A OR B AND tenant_id = ?
//
// 按 SQL 优先级即 `A OR (B AND tenant_id = ?)`——另一个租户的行只要满足 A 就被返回。
//
// 不依赖 GORM 自带的括号补全：它只在表达式是 Expr 且其 SQL 文本含 " OR " 时生效，
// 而那是对大写化文本做子串匹配。`Where("a = ?\nOR b = ?")` 里 OR 前面是换行符，
// 匹配不到，括号不会补——把越权的触发条件变成「有没有人换行写 SQL」。
// 本类型无条件加括号，与调用方怎么写无关。
//
// 内层复用 clause.Where.Build：它只负责拼接表达式、不写 "WHERE" 关键字
// （关键字来自 Clause.Build 对 Name() 的处理），因此 AND/OR 的连接语义
// 与 GORM 自身完全一致——这里只多了一对括号。
type group struct {
	inner clause.Where
}

func (g group) Build(builder clause.Builder) {
	// 返回值刻意丢弃：Builder 的写入面是 strings.Builder，它的 Write 永不出错，
	// GORM 自己的 clause 实现同样不检查。
	_ = builder.WriteByte('(')
	g.inner.Build(builder)
	_ = builder.WriteByte(')')
}

// injectCondition 把 `<表>.<列> = ?` 与既有条件以 AND 并列写回 WHERE。
//
// 值经 clause.Eq 的 Value 传递，由 GORM 生成绑定参数——不做任何字符串拼接。
// 列名带 clause.CurrentTable：JOIN 与别名场景下裸列名会歧义。
func injectCondition(stmt *gorm.Statement, column, value string) {
	cond := clause.Eq{
		Column: clause.Column{Table: clause.CurrentTable, Name: column},
		Value:  value,
	}

	existing, ok := stmt.Clauses[whereClause]
	if !ok {
		stmt.AddClause(clause.Where{Exprs: []clause.Expression{cond}})
		return
	}
	where, ok := existing.Expression.(clause.Where)
	if !ok || len(where.Exprs) == 0 {
		stmt.AddClause(clause.Where{Exprs: []clause.Expression{cond}})
		return
	}

	// 只替换 Expression，保留 Clause 的其余字段（Name、前后置表达式）。
	existing.Expression = clause.Where{
		Exprs: []clause.Expression{group{inner: where}, cond},
	}
	stmt.Clauses[whereClause] = existing
}

// hasUserCondition 报告调用方是否给出了自己的 WHERE 条件。
//
// 只数调用方的条件。此刻软删除条件尚未加入——GORM 的 SoftDeleteQueryClause /
// SoftDeleteDeleteClause 在 SQL 构建阶段才改写语句，晚于本插件的 Before 回调——
// 因此这里看到的 Exprs 里没有它。租户条件也还没注入。
// 两者都不该被算作「调用方给了条件」，否则这道保护就成了摆设。
func hasUserCondition(stmt *gorm.Statement) bool {
	existing, ok := stmt.Clauses[whereClause]
	if !ok {
		return false
	}
	where, ok := existing.Expression.(clause.Where)
	if !ok {
		return false
	}
	return len(where.Exprs) > 0
}
