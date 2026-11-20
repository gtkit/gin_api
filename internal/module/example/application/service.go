// Package application implements an owner-scoped record example.
package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/gtkit/gin-api/internal/pkg/paging"
)

var (
	ErrInvalid   = errors.New("invalid record input")
	ErrNotFound  = errors.New("record not found")
	ErrForbidden = errors.New("authenticated owner required")
)

type Record struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Page struct {
	Items  []Record      `json:"items"`
	Paging paging.Paging `json:"paging"`
}

// Repository scopes every operation to an authenticated owner.
type Repository interface {
	Find(context.Context, int64, string) (Record, bool, error)
	List(context.Context, int64, int, int) ([]Record, int64, error)
	Create(context.Context, int64, Record) error
	Update(context.Context, int64, Record) (bool, error)
	Delete(context.Context, int64, string) (bool, error)
	DeleteMany(context.Context, int64, []string) (int64, error)
}

type TxManager interface {
	Do(context.Context, func(context.Context) error) error
}

type Service struct {
	repo Repository
	tx   TxManager
}

func New(repo Repository, tx TxManager) (*Service, error) {
	if repo == nil || tx == nil {
		return nil, errors.New("record repository and transaction manager required")
	}
	return &Service{repo: repo, tx: tx}, nil
}

func validOwner(ctx context.Context, owner int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner <= 0 {
		return ErrForbidden
	}
	return nil
}

func validID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil() && parsed.String() == id
}

func title(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || value == "" || utf8.RuneCountInString(value) > 120 {
		return "", ErrInvalid
	}
	return value, nil
}

func (s *Service) Get(ctx context.Context, request GetRequest) (Record, error) {
	if err := validOwner(ctx, request.OwnerID); err != nil {
		return Record{}, err
	}
	if !validID(request.ID) {
		return Record{}, ErrInvalid
	}
	record, found, err := s.repo.Find(ctx, request.OwnerID, request.ID)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, ErrNotFound
	}
	return record, nil
}

func (s *Service) List(ctx context.Context, request ListRequest) (Page, error) {
	if err := validOwner(ctx, request.OwnerID); err != nil {
		return Page{}, err
	}
	page := request.Page
	if page == 0 {
		page = 1
	}
	size := request.Size
	if size == 0 {
		size = 20
	}
	if page < 1 || page > 10000 || size < 1 || size > 100 {
		return Page{}, ErrInvalid
	}
	records, total, err := s.repo.List(ctx, request.OwnerID, (page-1)*size, size)
	if err != nil {
		return Page{}, err
	}
	if records == nil {
		records = []Record{}
	}
	pages := int(total / int64(size))
	if total%int64(size) != 0 {
		pages++
	}
	return Page{Items: records, Paging: paging.Paging{CurrentPage: page, PageSize: size, TotalCount: total, TotalPage: pages}}, nil
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (Record, error) {
	if err := validOwner(ctx, request.OwnerID); err != nil {
		return Record{}, err
	}
	value, err := title(request.Title)
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	record := Record{ID: uuid.NewV7().String(), Title: value, CreatedAt: now, UpdatedAt: now}
	err = s.repo.Create(ctx, request.OwnerID, record)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Service) Update(ctx context.Context, request UpdateRequest) (Record, error) {
	if err := validOwner(ctx, request.OwnerID); err != nil {
		return Record{}, err
	}
	if !validID(request.ID) {
		return Record{}, ErrInvalid
	}
	value, err := title(request.Title)
	if err != nil {
		return Record{}, err
	}
	var record Record
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		var err error
		record, err = s.Get(ctx, GetRequest{OwnerID: request.OwnerID, ID: request.ID})
		if err != nil {
			return err
		}
		record.Title, record.UpdatedAt = value, time.Now().UTC()
		changed, err := s.repo.Update(ctx, request.OwnerID, record)
		if err == nil && !changed {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Service) Delete(ctx context.Context, request DeleteRequest) error {
	if err := validOwner(ctx, request.OwnerID); err != nil {
		return err
	}
	if !validID(request.ID) {
		return ErrInvalid
	}
	changed, err := s.repo.Delete(ctx, request.OwnerID, request.ID)
	if err == nil && !changed {
		return ErrNotFound
	}
	return err
}

// DeleteMany is all-or-nothing, including when any requested record belongs to another owner.
func (s *Service) DeleteMany(ctx context.Context, request DeleteManyRequest) error {
	if err := validOwner(ctx, request.OwnerID); err != nil {
		return err
	}
	if len(request.IDs) == 0 || len(request.IDs) > 100 {
		return ErrInvalid
	}
	seen := make(map[string]struct{}, len(request.IDs))
	for _, id := range request.IDs {
		if !validID(id) {
			return ErrInvalid
		}
		if _, exists := seen[id]; exists {
			return ErrInvalid
		}
		seen[id] = struct{}{}
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		deleted, err := s.repo.DeleteMany(ctx, request.OwnerID, request.IDs)
		if err != nil {
			return err
		}
		if deleted != int64(len(request.IDs)) {
			return ErrNotFound
		}
		return nil
	})
}
