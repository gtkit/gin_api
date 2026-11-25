package models

import "time"

const (
	FileUploadOperation  = "upload"
	FileMergeOperation   = "merge"
	FileDeleteOperation  = "delete"
	FileAttemptOperation = "attempt"

	FileOperationWorking = "working"
	FileOperationPending = "pending"
	FileOperationDone    = "done"
	FileOperationReview  = "review"
)

// FileOperation is the durable record of an object change and its publication.
// Child rows preserve each registered write location before object I/O.
type FileOperation struct {
	ID          string     `gorm:"type:char(36);primaryKey"`
	ParentID    *string    `gorm:"type:char(36);uniqueIndex:uk_file_op_attempt,priority:1"`
	OwnerID     int64      `gorm:"not null;uniqueIndex:uk_file_op_business,priority:2;index:idx_file_op_file,priority:1"`
	Kind        string     `gorm:"size:12;not null;uniqueIndex:uk_file_op_business,priority:1"`
	BusinessKey string     `gorm:"size:80;not null;uniqueIndex:uk_file_op_business,priority:3"`
	FileID      string     `gorm:"type:char(36);not null;index:idx_file_op_file,priority:2"`
	Driver      string     `gorm:"size:32;not null"`
	Bucket      string     `gorm:"size:128;not null"`
	Namespace   string     `gorm:"type:char(64);not null;index:idx_file_op_due,priority:1"`
	StorageKey  string     `gorm:"size:512;not null"`
	FileName    string     `gorm:"size:255;not null"`
	Size        int64      `gorm:"not null"`
	Checksum    string     `gorm:"size:128;not null"`
	Algorithm   string     `gorm:"size:16;not null"`
	State       string     `gorm:"size:12;not null;index:idx_file_op_due,priority:2"`
	Generation  uint64     `gorm:"not null;uniqueIndex:uk_file_op_attempt,priority:2"`
	LeaseUntil  *time.Time `gorm:"type:datetime(6)"`
	NextAt      time.Time  `gorm:"type:datetime(6);not null;index:idx_file_op_due,priority:3"`
	Failure     string     `gorm:"size:32;not null"`
	CreatedAt   time.Time  `gorm:"type:datetime(6);not null"`
	UpdatedAt   time.Time  `gorm:"type:datetime(6);not null"`
}

func (FileOperation) TableName() string { return "file_operations" }
