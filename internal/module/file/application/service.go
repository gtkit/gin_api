// Package application implements owner-scoped file upload and download.
package application

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/gtkit/gin-api/internal/pkg/paging"
)

var (
	ErrInvalid                 = errors.New("invalid file input")
	ErrNotFound                = errors.New("file not found")
	ErrForbidden               = errors.New("authenticated owner required")
	ErrStorageLocationMismatch = errors.New("file stored at a different storage location")
	ErrDependencyUnavailable   = errors.New("file data dependency unavailable")
)

// Record 是文件记录的应用层视图，直接作为 API 响应载荷。
// OwnerID/StorageKey 供持久化与回源使用，不对客户端暴露。
type Record struct {
	ID         string    `json:"id"`
	OwnerID    int64     `json:"-"`
	StorageKey string    `json:"-"`
	FileName   string    `json:"file_name"`
	Size       int64     `json:"size"`
	Driver     string    `json:"driver"`
	Bucket     string    `json:"bucket,omitempty"`
	Checksum   string    `json:"checksum,omitempty"`
	MIMEType   string    `json:"mime_type"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Page struct {
	Items  []Record      `json:"items"`
	Paging paging.Paging `json:"paging"`
}

// Repository scopes every operation to an authenticated owner.
// Create 对同一 (owner, storage_key) 的重复写入按幂等成功处理。
type Repository interface {
	Create(context.Context, Record) (Record, error)
	Find(ctx context.Context, owner int64, id string) (Record, bool, error)
	ListAfter(ctx context.Context, owner int64, at time.Time, id string, size int) ([]Record, error)
	List(ctx context.Context, owner int64, offset, limit int) ([]Record, int64, error)
	Delete(ctx context.Context, owner int64, id string) (bool, error)
}

type Service struct {
	transfer   Transfer
	repo       Repository
	driver     string
	bucket     string
	operations *OperationOptions
}

// New 装配文件服务。driver/bucket 是当前部署的存储位置，写入每条记录
// 用于溯源；历史记录据此拒绝跨存储位置读写。
func New(transfer Transfer, repo Repository, driver, bucket string, options ...OperationOptions) (*Service, error) {
	if transfer == nil || repo == nil {
		return nil, errors.New("file uploader and repository required")
	}
	if driver == "" {
		return nil, errors.New("file storage driver required")
	}
	service := &Service{transfer: transfer, repo: repo, driver: driver, bucket: bucket}
	if len(options) != 0 {
		opt := options[0]
		if opt.Repository == nil || opt.Namespace == "" || opt.Session == nil ||
			opt.MaxFileBytes <= 0 || len(opt.AllowedTypes) == 0 {
			return nil, errors.New("complete file recovery configuration is required")
		}
		service.operations = &opt
	}
	return service, nil
}

// Upload 处理普通单文件上传：流式写入当前驱动并建立归属记录。
func (s *Service) Upload(ctx context.Context, owner int64, fileName string, reader io.Reader, size int64) (Record, error) {
	if err := validOwner(ctx, owner); err != nil {
		return Record{}, err
	}
	if s.operations != nil {
		source, ok := reader.(io.ReadSeeker)
		if !ok {
			return Record{}, ErrInvalid
		}
		return s.uploadRecoverable(ctx, owner, fileName, source)
	}
	result, err := s.transfer.Upload(ctx, owner, fileName, reader, size)
	if err != nil {
		return Record{}, err
	}
	return s.recordResult(ctx, owner, result)
}

// ChunkInit 初始化分片会话。
func (s *Service) ChunkInit(ctx context.Context, owner int64, req *ChunkInitRequest) (*ChunkInitResult, error) {
	if err := validOwner(ctx, owner); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrInvalid
	}
	return s.transfer.ChunkInit(ctx, owner, req)
}

// UploadChunk 上传单个分片（代理路径，chunkIndex 0-based）。
func (s *Service) UploadChunk(ctx context.Context, owner int64, uploadID string, chunkIndex int, reader io.Reader, size int64, checksum string) (*ChunkUploadResult, error) {
	if err := validOwner(ctx, owner); err != nil {
		return nil, err
	}
	if s.operations != nil {
		if err := s.requireChunkLocation(ctx, owner, uploadID); err != nil {
			return nil, err
		}
	}
	return s.transfer.UploadChunk(ctx, owner, uploadID, chunkIndex, reader, size, checksum)
}

// Status 查询分片进度。
func (s *Service) Status(ctx context.Context, owner int64, uploadID string) (*ChunkStatusResult, error) {
	if err := validOwner(ctx, owner); err != nil {
		return nil, err
	}
	return s.transfer.Status(ctx, owner, uploadID)
}

// Resume 按整文件哈希找回未完成会话（断点续传）或命中已完成对象。
func (s *Service) Resume(ctx context.Context, owner int64, req *ResumeRequest) (*ResumeResult, error) {
	if err := validOwner(ctx, owner); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrInvalid
	}
	return s.transfer.Resume(ctx, owner, req)
}

// Abort 中止会话并清理分片数据。
func (s *Service) Abort(ctx context.Context, owner int64, uploadID string) error {
	if err := validOwner(ctx, owner); err != nil {
		return err
	}
	if s.operations != nil {
		if err := s.requireChunkLocation(ctx, owner, uploadID); err != nil {
			if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrSessionExpired) {
				return nil
			}
			return err
		}
	}
	return s.transfer.Abort(ctx, owner, uploadID)
}

// Merge publishes the owner-scoped file after the registered object is complete.
func (s *Service) Merge(ctx context.Context, owner int64, uploadID string) (Record, error) {
	if err := validOwner(ctx, owner); err != nil {
		return Record{}, err
	}
	if s.operations != nil {
		return s.mergeRecoverable(ctx, owner, uploadID)
	}
	result, err := s.transfer.Merge(ctx, owner, uploadID, nil)
	if err != nil {
		return Record{}, err
	}
	return s.recordResult(ctx, owner, result)
}

// Get 返回归属用户的文件记录。
func (s *Service) Get(ctx context.Context, owner int64, id string) (Record, error) {
	if err := validOwner(ctx, owner); err != nil {
		return Record{}, err
	}
	if !validID(id) {
		return Record{}, ErrNotFound
	}
	record, found, err := s.repo.Find(ctx, owner, id)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, ErrNotFound
	}
	return record, nil
}

// List 分页返回归属用户的文件记录。
func (s *Service) List(ctx context.Context, owner int64, page, size int) (Page, error) {
	if err := validOwner(ctx, owner); err != nil {
		return Page{}, err
	}
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = 20
	}
	if page < 1 || page > 10000 || size < 1 || size > 100 {
		return Page{}, ErrInvalid
	}
	records, total, err := s.repo.List(ctx, owner, (page-1)*size, size)
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

// CursorPage returns an empty next_cursor on the last page.
type CursorPage struct {
	Items      []Record `json:"items"`
	NextCursor string   `json:"next_cursor"`
}

// ListAfter traverses files in descending creation time and ID order.
// The authenticated owner is always passed independently to the repository.
func (s *Service) ListAfter(ctx context.Context, owner int64, cursor string, size int) (CursorPage, error) {
	if err := validOwner(ctx, owner); err != nil {
		return CursorPage{}, err
	}
	if size == 0 {
		size = 20
	}
	if size < 1 || size > 100 || len(cursor) > 160 {
		return CursorPage{}, ErrInvalid
	}
	var at time.Time
	var id string
	ownerKey := strconv.FormatInt(owner, 10)
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		cursorOwner, position, found := strings.Cut(string(raw), "|")
		stamp, fileID, hasID := strings.Cut(position, "|")
		if err != nil || !found || cursorOwner != ownerKey || !hasID || !validID(fileID) {
			return CursorPage{}, ErrInvalid
		}
		at, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return CursorPage{}, ErrInvalid
		}
		id = fileID
	}
	items, err := s.repo.ListAfter(ctx, owner, at, id, size)
	if err != nil {
		return CursorPage{}, err
	}
	if items == nil {
		items = []Record{}
	}
	page := CursorPage{Items: items}
	if len(items) > size {
		page.Items = items[:size]
		last := items[size-1]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(ownerKey + "|" + last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID))
	}
	return page, nil
}

// Delete hides the owner-scoped file before deleting its object when recovery is wired.
func (s *Service) Delete(ctx context.Context, owner int64, id string) error {
	if s.operations != nil {
		return s.deleteRecoverable(ctx, owner, id)
	}
	record, err := s.Get(ctx, owner, id)
	if err != nil {
		return err
	}
	if !s.matchesStorageLocation(record) {
		return ErrStorageLocationMismatch
	}
	if err := s.transfer.Delete(ctx, record.StorageKey); err != nil {
		return fmt.Errorf("delete storage object: %w", err)
	}
	deleted, err := s.repo.Delete(ctx, owner, id)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	return nil
}

// Download 归属校验通过后流式回源；调用方负责关闭返回的流。
func (s *Service) Download(ctx context.Context, owner int64, id string) (Record, io.ReadCloser, error) {
	record, err := s.Get(ctx, owner, id)
	if err != nil {
		return Record{}, nil, err
	}
	if !s.matchesStorageLocation(record) {
		return Record{}, nil, ErrStorageLocationMismatch
	}
	if s.operations != nil {
		if err := s.checkPublishedLocation(ctx, owner, id); err != nil {
			return Record{}, nil, err
		}
	}
	rc, err := s.transfer.Get(ctx, record.StorageKey)
	if err != nil {
		return Record{}, nil, fmt.Errorf("open storage object: %w", err)
	}
	return record, rc, nil
}

func (s *Service) matchesStorageLocation(record Record) bool {
	if record.Driver != s.driver || record.Bucket != s.bucket {
		return false
	}
	if s.driver == "local" {
		return record.Bucket == ""
	}
	return record.Bucket != ""
}

// recordResult 把引擎结果落成归属记录并返回规范记录（幂等重放时
// 返回已有行而不是新 ID）。
func (s *Service) recordResult(ctx context.Context, owner int64, result *ObjectResult) (Record, error) {
	now := time.Now().UTC()
	record := Record{
		ID:         uuid.NewV7().String(),
		OwnerID:    owner,
		StorageKey: result.StorageKey,
		FileName:   result.FileName,
		Size:       result.FileSize,
		Driver:     s.driver,
		Bucket:     s.bucket,
		Checksum:   result.Checksum,
		MIMEType:   mimeFromName(result.FileName),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	stored, err := s.repo.Create(ctx, record)
	if err != nil {
		return Record{}, fmt.Errorf("record uploaded file: %w", err)
	}
	return stored, nil
}

func mimeFromName(fileName string) string {
	if mt := mime.TypeByExtension(filepath.Ext(fileName)); mt != "" {
		return mt
	}
	return "application/octet-stream"
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
