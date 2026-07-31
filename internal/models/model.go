package models

import (
	"time"

	"gorm.io/gorm"
)

type BaseID struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement;comment:主键编码" json:"id"`
}

// BaseTimeField 时间戳.
type BaseTimeField struct {
	CreatedAt time.Time      `gorm:"column:created_at;type:datetime;index;comment:创建时间" json:"created_at,omitempty"`
	UpdatedAt time.Time      `gorm:"column:updated_at;type:datetime;index;comment:最后更新时间" json:"updated_at,omitempty"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;type:datetime;index;comment:删除时间" json:"-"`
}
