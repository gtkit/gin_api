package models

import (
	"time"

	"gorm.io/gorm"
)

// File 是上传对象的归属记录。Never bind HTTP requests to this model.
//
// Driver/Bucket 记录上传时的存储位置：单活驱动部署切换后，
// 历史记录据此识别「对象不在当前驱动」，而不是静默读错存储。
type File struct {
	ID         string         `gorm:"type:char(36);primaryKey"`
	OwnerID    int64          `gorm:"not null;index:idx_files_owner,priority:1;uniqueIndex:uk_files_owner_storage,priority:1"`
	FileName   string         `gorm:"size:255;not null"`
	StorageKey string         `gorm:"size:512;not null;uniqueIndex:uk_files_owner_storage,priority:2"`
	Driver     string         `gorm:"size:32;not null"`
	Bucket     string         `gorm:"size:128;not null;default:''"`
	Size       int64          `gorm:"not null"`
	Checksum   string         `gorm:"size:128;not null;default:''"`
	MIMEType   string         `gorm:"size:128;not null;default:''"`
	CreatedAt  time.Time      `gorm:"not null;index:idx_files_owner,priority:3"`
	UpdatedAt  time.Time      `gorm:"not null"`
	DeletedAt  gorm.DeletedAt `gorm:"index:idx_files_owner,priority:2"`
}

func (File) TableName() string { return "files" }
