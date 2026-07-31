package file

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/time/rate"
)

type FileService struct {
	storagePath string
	mutex       sync.RWMutex
	maxFileSize int64
}

func (fs *FileService) Upload(fileHeader *multipart.FileHeader, customPath string) (string, error) {
	if fileHeader.Size > fs.maxFileSize {
		return "", errors.New("文件大小超过限制")
	}

	safeName := GenerateSafeFileName(fileHeader.Filename)
	fullPath := filepath.Join(fs.storagePath, customPath, safeName)
	if !fs.isPathValid(fullPath) {
		return "", errors.New("非法存储路径")
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}

	src, err := fileHeader.Open()
	if err != nil {
		return "", fmt.Errorf("打开上传文件失败: %w", err)
	}
	defer src.Close()

	dst, err := os.Create(fullPath)
	if err != nil {
		return "", fmt.Errorf("创建目标文件失败: %w", err)
	}
	defer dst.Close()

	if _, err = fs.bufferedCopy(dst, src); err != nil {
		return "", fmt.Errorf("文件复制失败: %w", err)
	}

	return safeName, nil
}

func (fs *FileService) Download(w http.ResponseWriter, filePath string) error {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	fullPath := filepath.Join(fs.storagePath, filePath)
	if !fs.isPathValid(fullPath) {
		return errors.New("非法文件路径")
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return fmt.Errorf("文件打开失败: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("获取文件信息失败: %w", err)
	}

	w.Header().Set("Content-Disposition", "attachment; filename="+stat.Name())
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))

	if _, err = fs.bufferedCopy(w, file); err != nil {
		return fmt.Errorf("文件传输失败: %w", err)
	}

	return nil
}

type ChunkInfo struct {
	UploadID    string `json:"upload_id" validate:"required"`
	ChunkNumber int
	TotalChunks int
}

func (fs *FileService) ChunkUpload(chunk *multipart.FileHeader, chunkInfo ChunkInfo) error {
	chunkDir := filepath.Join(fs.storagePath, "chunks", chunkInfo.UploadID)
	if !fs.isPathValid(chunkDir) {
		return errors.New("非法分片路径")
	}

	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		return fmt.Errorf("创建分片目录失败: %w", err)
	}

	chunkPath := filepath.Join(chunkDir, fmt.Sprintf("%d.chunk", chunkInfo.ChunkNumber))
	if err := fs.saveUploadedFile(chunk, chunkPath); err != nil {
		return fmt.Errorf("保存分片失败: %w", err)
	}

	return nil
}

func (fs *FileService) saveUploadedFile(file *multipart.FileHeader, path string) (err error) {
	src, err := file.Open()
	if err != nil {
		return fmt.Errorf("打开上传文件失败: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("创建目标文件失败: %w", err)
	}
	defer func() {
		closeErr := dst.Close()
		if closeErr != nil && err == nil {
			err = fmt.Errorf("关闭目标文件失败: %w", closeErr)
		}
	}()

	if _, copyErr := fs.bufferedCopy(dst, src); copyErr != nil {
		err = fmt.Errorf("文件复制失败: %w", copyErr)
		return err
	}

	return nil
}

func (fs *FileService) MergeChunks(uploadID, targetPath string) error {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	chunkDir := filepath.Join(fs.storagePath, "chunks", uploadID)
	targetFile := filepath.Join(fs.storagePath, targetPath)
	if !fs.isPathValid(chunkDir) || !fs.isPathValid(targetFile) {
		return errors.New("非法分片路径")
	}

	if err := os.MkdirAll(filepath.Dir(targetFile), 0o755); err != nil {
		return fmt.Errorf("创建目标目录失败: %w", err)
	}

	chunks, err := os.ReadDir(chunkDir)
	if err != nil {
		return fmt.Errorf("读取分片目录失败: %w", err)
	}

	type chunkEntry struct {
		name   string
		number int
	}

	orderedChunks := make([]chunkEntry, 0, len(chunks))
	for _, chunk := range chunks {
		number, err := parseChunkNumber(chunk.Name())
		if err != nil {
			return fmt.Errorf("解析分片名称失败: %w", err)
		}
		orderedChunks = append(orderedChunks, chunkEntry{
			name:   chunk.Name(),
			number: number,
		})
	}

	sort.Slice(orderedChunks, func(i, j int) bool {
		return orderedChunks[i].number < orderedChunks[j].number
	})

	dst, err := os.Create(targetFile)
	if err != nil {
		return fmt.Errorf("创建目标文件失败: %w", err)
	}
	defer dst.Close()

	for _, chunk := range orderedChunks {
		chunkPath := filepath.Join(chunkDir, chunk.name)
		src, err := os.Open(chunkPath)
		if err != nil {
			return fmt.Errorf("打开分片失败: %w", err)
		}

		if _, err = fs.bufferedCopy(dst, src); err != nil {
			src.Close()
			return fmt.Errorf("合并分片失败: %w", err)
		}
		src.Close()
	}

	if err := os.RemoveAll(chunkDir); err != nil {
		return fmt.Errorf("清理分片失败: %w", err)
	}

	return nil
}

type RateLimitedFileService struct {
	*FileService
	uploadLimiter   *rate.Limiter
	downloadLimiter *rate.Limiter
}

func NewFileService(storagePath string) *FileService {
	return &FileService{
		storagePath: storagePath,
		maxFileSize: 10 * 1024 * 1024,
	}
}

func NewRateLimitedFileService(storagePath string, uploadRate, downloadRate rate.Limit) *RateLimitedFileService {
	return &RateLimitedFileService{
		FileService:     NewFileService(storagePath),
		uploadLimiter:   rate.NewLimiter(uploadRate, int(uploadRate)),
		downloadLimiter: rate.NewLimiter(downloadRate, int(downloadRate)),
	}
}

func (r *RateLimitedFileService) Upload(fileHeader *multipart.FileHeader, customPath string) (string, error) {
	if !r.uploadLimiter.Allow() {
		return "", errors.New("上传速率超过限制")
	}

	return r.FileService.Upload(fileHeader, customPath)
}

func (r *RateLimitedFileService) Download(w http.ResponseWriter, filePath string) error {
	if !r.downloadLimiter.Allow() {
		return errors.New("下载速率超过限制")
	}

	return r.FileService.Download(w, filePath)
}

func (fs *FileService) isPathValid(path string) bool {
	return IsPathValid(fs.storagePath, path)
}

var bufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

func (fs *FileService) bufferedCopy(dst io.Writer, src io.Reader) (int64, error) {
	bufPtr, ok := bufPool.Get().(*[]byte)
	if !ok {
		buf := make([]byte, 32*1024)
		bufPtr = &buf
	}
	defer bufPool.Put(bufPtr)

	return io.CopyBuffer(dst, src, *bufPtr)
}

var errZeroCopyUnavailable = errors.New("zero-copy send unavailable")

func (fs *FileService) ZeroCopyDownload(w http.ResponseWriter, filePath string) error {
	fullPath := filepath.Join(fs.storagePath, filePath)
	if !fs.isPathValid(fullPath) {
		return errors.New("非法文件路径")
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}

	w.Header().Set("Content-Disposition", "attachment; filename="+stat.Name())
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))

	if tcpConn, ok := w.(interface{ File() (*os.File, error) }); ok {
		connFile, err := tcpConn.File()
		if err != nil {
			return err
		}
		defer connFile.Close()

		if err = trySendfile(connFile, file, stat.Size()); err == nil {
			return nil
		}
		if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
			return seekErr
		}
	}

	_, err = fs.bufferedCopy(w, file)
	return err
}

var allowedMimeTypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"application/pdf": true,
	"text/plain":      true,
}

func parseChunkNumber(name string) (int, error) {
	if filepath.Ext(name) != ".chunk" {
		return 0, fmt.Errorf("invalid chunk file %q", name)
	}

	chunkNumber, err := strconv.Atoi(strings.TrimSuffix(name, ".chunk"))
	if err != nil || chunkNumber < 0 {
		return 0, fmt.Errorf("invalid chunk file %q", name)
	}

	return chunkNumber, nil
}
