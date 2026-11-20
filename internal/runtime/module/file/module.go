// Package file 装配 file 模块：把 gouploader 上传器与 files 仓储
// 注入 application service。
package file

import (
	"context"
	"errors"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/module/file/application"
	filerepo "github.com/gtkit/gin-api/internal/repository/file"
	"github.com/gtkit/gouploader"
	"gorm.io/gorm"
)

func New(up *gouploader.Uploader, db *gorm.DB, driver, bucket string) (*application.Service, error) {
	if db == nil {
		return nil, errors.New("file database is required")
	}
	return application.New(up, adapter{repo: filerepo.New(db)}, driver, bucket)
}

type adapter struct{ repo *filerepo.Repository }

func value(record models.File) application.Record {
	return application.Record{
		ID:         record.ID,
		OwnerID:    record.OwnerID,
		StorageKey: record.StorageKey,
		FileName:   record.FileName,
		Size:       record.Size,
		Driver:     record.Driver,
		Bucket:     record.Bucket,
		Checksum:   record.Checksum,
		MIMEType:   record.MIMEType,
		CreatedAt:  record.CreatedAt,
		UpdatedAt:  record.UpdatedAt,
	}
}

func model(record application.Record) models.File {
	return models.File{
		ID:         record.ID,
		OwnerID:    record.OwnerID,
		StorageKey: record.StorageKey,
		FileName:   record.FileName,
		Size:       record.Size,
		Driver:     record.Driver,
		Bucket:     record.Bucket,
		Checksum:   record.Checksum,
		MIMEType:   record.MIMEType,
		CreatedAt:  record.CreatedAt,
		UpdatedAt:  record.UpdatedAt,
	}
}

func (a adapter) Create(ctx context.Context, record application.Record) (application.Record, error) {
	stored, err := a.repo.Create(ctx, model(record))
	return value(stored), err
}

func (a adapter) Find(ctx context.Context, owner int64, id string) (application.Record, bool, error) {
	record, found, err := a.repo.Find(ctx, owner, id)
	return value(record), found, err
}

func (a adapter) List(ctx context.Context, owner int64, offset, limit int) ([]application.Record, int64, error) {
	records, total, err := a.repo.List(ctx, owner, offset, limit)
	values := make([]application.Record, 0, len(records))
	for _, record := range records {
		values = append(values, value(record))
	}
	return values, total, err
}

func (a adapter) Delete(ctx context.Context, owner int64, id string) (bool, error) {
	return a.repo.Delete(ctx, owner, id)
}

var _ application.Repository = adapter{}
