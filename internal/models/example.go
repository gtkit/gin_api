package models

import (
	"time"

	"gorm.io/gorm"
)

// ExampleRecord is the persisted record. Never bind HTTP requests to this model.
type ExampleRecord struct {
	ID        string         `gorm:"type:char(36);primaryKey;index:idx_example_owner,priority:4"`
	OwnerID   int64          `gorm:"not null;index:idx_example_owner,priority:1"`
	Title     string         `gorm:"size:120;not null"`
	CreatedAt time.Time      `gorm:"not null;index:idx_example_owner,priority:3"`
	UpdatedAt time.Time      `gorm:"not null"`
	DeletedAt gorm.DeletedAt `gorm:"index:idx_example_owner,priority:2"`
}

func (ExampleRecord) TableName() string { return "example_records" }
