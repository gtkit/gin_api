package models

import (
	"time"

	"gorm.io/gorm"
)

type BaseID struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement;comment:主键编码" json:"id"`
}

// BaseTimeField 时间戳.
//
// 时间字段用 omitzero 而不是 omitempty：encoding/json 的 omitempty 对 struct 类型
// **永不触发**，零值时间照样输出 "0001-01-01T00:00:00Z"，标注写了却不生效。
// omitzero（Go 1.24 起）按 IsZero() 判定，是这里唯一能兑现"零值省略"的写法。
// 生产构建带 -tags=sonic，已实测 sonic v1.15.2 与标准库行为一致。
type BaseTimeField struct {
	CreatedAt time.Time      `gorm:"column:created_at;type:datetime;index;comment:创建时间" json:"created_at,omitzero"`
	UpdatedAt time.Time      `gorm:"column:updated_at;type:datetime;index;comment:最后更新时间" json:"updated_at,omitzero"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;type:datetime;index;comment:删除时间" json:"-"`
}
