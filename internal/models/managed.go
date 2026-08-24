package models

// Managed 返回本库全部受管模型。
//
// 谁需要它：需要"这个库有哪些表、每张表有哪些列"这类**集合级**事实的能力。
// 目前是租户数据隔离——它靠这份清单在装配期核对判别列确实存在于某个模型上，
// 以及豁免清单里的表名确实存在。两项校验在运行期都无法补救：列名写错时
// 判定永不命中，于是零张表受控，隔离静默消失而一切看起来正常。
//
// 清单的完整性由 managed_test.go 的枚举判据保证：`internal/models` 下每个
// 实现了 TableName() 的类型都必须出现在这里。漏项是清单类缺陷里最安静的一种——
// 使用者不会去找一个他不知道存在的东西。
//
// 已知的重复：cmd/migrate.go 的 WithDDLModels 另有一份同样内容的清单，
// 供迁移与 DDL 生成使用，并由它自己的枚举判据看住。两份清单各有判据，
// 因此都不会无声腐烂；把它们收敛成一份是一次独立的改动。
func Managed() []any {
	return []any{
		&User{},
		&UserProfile{},
		&UserDeactivation{},
		&UserRole{},
		&OperationAudit{},
		// scaffold:managed-models
	}
}
