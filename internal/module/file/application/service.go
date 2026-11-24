// Package application implements owner-scoped file upload and download.
//
// 上传引擎是 gouploader（传输无关、无 Gin）：协议类型直接复用其定义，
// 文件持久化经本包 Repository port，adapter 在 runtime/module/file。
// 每次引擎调用前把请求者身份注入请求 context（UploadInfo），库侧据此
// 校验分片会话归属；文件记录的归属校验在本包完成。
package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"strconv"
	"time"
	"uuid"

	"github.com/gtkit/gin-api/internal/pkg/paging"
	"github.com/gtkit/gouploader"
)

var (
	ErrInvalid               = errors.New("invalid file input")
	ErrNotFound              = errors.New("file not found")
	ErrForbidden             = errors.New("authenticated owner required")
	ErrDriverMismatch        = errors.New("file stored on a different storage driver")
	ErrDependencyUnavailable = errors.New("file data dependency unavailable")
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
	List(ctx context.Context, owner int64, offset, limit int) ([]Record, int64, error)
	Delete(ctx context.Context, owner int64, id string) (bool, error)
}

type Service struct {
	up     *gouploader.Uploader
	repo   Repository
	driver string
	bucket string
}

// New 装配文件服务。driver/bucket 是当前部署的存储位置，写入每条记录
// 用于溯源；driver 切换后历史记录据此拒绝跨驱动读写。
func New(up *gouploader.Uploader, repo Repository, driver, bucket string) (*Service, error) {
	if up == nil || repo == nil {
		return nil, errors.New("file uploader and repository required")
	}
	if driver == "" {
		return nil, errors.New("file storage driver required")
	}
	return &Service{up: up, repo: repo, driver: driver, bucket: bucket}, nil
}

// Upload 处理普通单文件上传：流式写入当前驱动并建立归属记录。
func (s *Service) Upload(ctx context.Context, owner int64, fileName string, reader io.Reader, size int64) (Record, error) {
	ctx, err := s.uploadContext(ctx, owner)
	if err != nil {
		return Record{}, err
	}
	result, err := s.up.UploadService().Upload(ctx, fileName, reader, size, "", "")
	if err != nil {
		return Record{}, err
	}
	return s.recordResult(ctx, owner, result)
}

// ChunkInit 初始化分片会话。
func (s *Service) ChunkInit(ctx context.Context, owner int64, req *gouploader.ChunkInitRequest) (*gouploader.ChunkInitResponse, error) {
	ctx, err := s.uploadContext(ctx, owner)
	if err != nil {
		return nil, err
	}
	return s.up.ChunkService().InitUpload(ctx, req)
}

// UploadChunk 上传单个分片（代理路径，chunkIndex 0-based）。
func (s *Service) UploadChunk(ctx context.Context, owner int64, uploadID string, chunkIndex int, reader io.Reader, size int64, checksum string) (*gouploader.ChunkUploadResponse, error) {
	ctx, err := s.uploadContext(ctx, owner)
	if err != nil {
		return nil, err
	}
	return s.up.ChunkService().UploadChunk(ctx, uploadID, chunkIndex, reader, size, checksum)
}

// Status 查询分片进度。
func (s *Service) Status(ctx context.Context, owner int64, uploadID string) (*gouploader.ChunkStatusResponse, error) {
	ctx, err := s.uploadContext(ctx, owner)
	if err != nil {
		return nil, err
	}
	return s.up.ChunkService().GetStatus(ctx, uploadID)
}

// Resume 按整文件哈希找回未完成会话（断点续传）或命中已完成对象。
func (s *Service) Resume(ctx context.Context, owner int64, req *gouploader.ResumeRequest) (*gouploader.ResumeResponse, error) {
	ctx, err := s.uploadContext(ctx, owner)
	if err != nil {
		return nil, err
	}
	return s.up.ChunkService().Resume(ctx, req)
}

// Abort 中止会话并清理分片数据。
func (s *Service) Abort(ctx context.Context, owner int64, uploadID string) error {
	ctx, err := s.uploadContext(ctx, owner)
	if err != nil {
		return err
	}
	return s.up.ChunkService().Abort(ctx, uploadID)
}

// Merge 合并分片并建立归属记录。重复合并由库返回缓存结果，
// 记录侧经 (owner, storage_key) 幂等，返回的都是同一条记录。
func (s *Service) Merge(ctx context.Context, owner int64, uploadID string) (Record, error) {
	ctx, err := s.uploadContext(ctx, owner)
	if err != nil {
		return Record{}, err
	}
	result, err := s.up.ChunkService().Merge(ctx, uploadID)
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

// Delete 先删存储对象、成功后软删记录；存储删除失败保留记录并返回错误，
// 避免记录指向已不存在的对象。
func (s *Service) Delete(ctx context.Context, owner int64, id string) error {
	record, err := s.Get(ctx, owner, id)
	if err != nil {
		return err
	}
	if record.Driver != s.driver {
		return ErrDriverMismatch
	}
	if err := s.up.Storage().Delete(ctx, record.StorageKey); err != nil {
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
	if record.Driver != s.driver {
		return Record{}, nil, ErrDriverMismatch
	}
	rc, err := s.up.Storage().Get(ctx, record.StorageKey)
	if err != nil {
		return Record{}, nil, fmt.Errorf("open storage object: %w", err)
	}
	return record, rc, nil
}

// uploadContext 校验身份并注入 UploadInfo：库侧 WithUserIDFn 从中解析
// 会话创建者。owner 非正即拒绝——库不拒绝匿名 init，硬校验在此完成。
func (s *Service) uploadContext(ctx context.Context, owner int64) (context.Context, error) {
	if err := validOwner(ctx, owner); err != nil {
		return nil, err
	}
	return gouploader.ContextWithUploadInfo(ctx, gouploader.UploadInfo{
		UserID: strconv.FormatInt(owner, 10),
	}), nil
}

// recordResult 把引擎结果落成归属记录并返回规范记录（幂等重放时
// 返回已有行而不是新 ID）。
func (s *Service) recordResult(ctx context.Context, owner int64, result *gouploader.UploadResult) (Record, error) {
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
