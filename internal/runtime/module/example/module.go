package example

import (
	"context"
	"errors"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/module/example/application"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
	recordrepo "github.com/gtkit/gin-api/internal/repository/example"
	"gorm.io/gorm"
)

func New(db *gorm.DB) (*application.Service, error) {
	if db == nil {
		return nil, errors.New("record database is required")
	}
	return application.New(adapter{repo: recordrepo.New(db)}, dbtx.NewManager(db))
}

type adapter struct{ repo *recordrepo.Repository }

func value(record models.ExampleRecord) application.Record {
	return application.Record{ID: record.ID, Title: record.Title, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}
func model(owner int64, record application.Record) models.ExampleRecord {
	return models.ExampleRecord{ID: record.ID, OwnerID: owner, Title: record.Title, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
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
func (a adapter) Create(ctx context.Context, owner int64, record application.Record) error {
	return a.repo.Create(ctx, model(owner, record))
}
func (a adapter) Update(ctx context.Context, owner int64, record application.Record) (bool, error) {
	return a.repo.Update(ctx, model(owner, record))
}
func (a adapter) Delete(ctx context.Context, owner int64, id string) (bool, error) {
	return a.repo.Delete(ctx, owner, id)
}
func (a adapter) DeleteMany(ctx context.Context, owner int64, ids []string) (int64, error) {
	return a.repo.DeleteMany(ctx, owner, ids)
}

var _ application.Repository = adapter{}
