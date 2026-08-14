package models

import "time"

// UserRole 是用户与角色的关联记录，权威角色源。
//
// **示例模型，可按需删除**（删除清单见 SCAFFOLD.md）。
//
// 为什么是独立关联表而不是 users 上的一个 role 列：单列只能表达一个角色，
// 而"一个用户多个角色"是这类需求几乎必然的下一步——届时要同时改表结构、
// 改判定并迁移存量数据。关联表从一开始就支持多角色，代价只是多一张表。
//
// 可删性同理：独立表删起来是"删表 + 删目录"，加列则要动 users 的模型、
// 迁移与既有查询。
type UserRole struct {
	BaseID

	UserID int64 `gorm:"column:user_id;not null;uniqueIndex:uk_user_roles_user_role,priority:1;comment:用户ID"`
	// Role 是角色标识，由业务定义。
	//
	// 它与授权注册表里的 ID（如 "role.admin"）**不是同一个东西**：
	// 前者是这张表里的值，后者是路由声明引用的标识。两者碰巧相近，
	// 但将来可能不再对应（例如 role.admin 改为检查 superuser）。
	Role string `gorm:"column:role;size:64;not null;uniqueIndex:uk_user_roles_user_role,priority:2;comment:角色标识"`

	CreatedAt time.Time `gorm:"column:created_at;type:datetime;comment:创建时间"`
}

// TableName 指定表名。
func (r *UserRole) TableName() string {
	return "user_roles"
}
