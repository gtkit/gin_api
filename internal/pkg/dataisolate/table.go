package dataisolate

import "gorm.io/gorm"

// controlled 判定本次语句的目标表是否受租户约束。
//
// 判定顺序：豁免 → 强制 → 语句自身的模型元数据 → 装配期登记的表 → fail-closed。
//
// 以语句自身的元数据为主判据（而不是装配期算好的表集合）有一个具体收益：
// 一个**未登记**但声明了判别列的模型仍然受保护。失效方向偏安全的那一侧。
//
// 装配期集合只在语句缺少元数据时兜底。两者都不认识这张表时报错而不是放行：
// 放行才是危险的那个选择——`db.Table("users").Where(…).Find()` 会完整绕过隔离，
// 而它不报错、不进日志，只在有人比对数据时才被发现。
func (p *Plugin) controlled(stmt *gorm.Statement) (bool, error) {
	table := stmt.Table

	// 没有目标表的语句不受控。
	//
	// `SELECT @@session.time_zone`、`SELECT 1` 这类语句压根不访问表，因此
	// 不可能泄漏任何租户的行——把它们 fail-closed 掉只会打断连接校验与健康检查，
	// 而那两件事恰恰在最需要它们能跑的时候（排障）被用到。
	//
	// **这不是放宽 Schema==nil 那条规则**，两者的输入不同：这里是"没有表"，
	// 那里是"指定了一张表但认不出来"。带表的裸 SQL（`Raw("SELECT * FROM users")`）
	// 会落在这一支放行——那正是包注释里声明的能力边界，由架构传感器禁止
	// 数据访问层使用裸 SQL 来执行，不在这里假装覆盖。
	if table == "" {
		return false, nil
	}

	if p.ignore[table] {
		return false, nil
	}
	if p.force[table] {
		return true, nil
	}
	if stmt.Schema != nil {
		return stmt.Schema.LookUpField(p.column) != nil, nil
	}
	if isControlled, registered := p.knownControlled[table]; registered {
		return isControlled, nil
	}
	return false, errUnknownTable
}
