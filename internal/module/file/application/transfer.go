package application

import (
	"context"
	"errors"
	"io"
)

var (
	ErrSessionNotFound    = errors.New("upload session not found")
	ErrSessionExpired     = errors.New("upload session expired")
	ErrSessionOwner       = errors.New("upload session belongs to another account")
	ErrFileTypeNotAllowed = errors.New("file type is not allowed")
	ErrFileTooLarge       = errors.New("file is too large")
	ErrChecksumMismatch   = errors.New("file checksum does not match")
	ErrMergeIncomplete    = errors.New("upload chunks are incomplete")
	ErrMergeInProgress    = errors.New("file merge in progress")
	ErrStorageUnavailable = errors.New("file storage is unavailable")
)

// Transfer adapts file and chunk I/O. Session methods receive the authenticated owner.
type Transfer interface {
	Upload(context.Context, int64, string, io.Reader, int64) (*ObjectResult, error)
	ChunkInit(context.Context, int64, *ChunkInitRequest) (*ChunkInitResult, error)
	UploadChunk(context.Context, int64, string, int, io.Reader, int64, string) (*ChunkUploadResult, error)
	Status(context.Context, int64, string) (*ChunkStatusResult, error)
	Resume(context.Context, int64, *ResumeRequest) (*ResumeResult, error)
	Abort(context.Context, int64, string) error
	Merge(context.Context, int64, string, func(context.Context, string) error) (*ObjectResult, error)
	Save(context.Context, int64, string, io.Reader, int64) error
	Get(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	Exists(context.Context, string) (bool, error)
}

// ObjectResult describes only the metadata needed to publish a file.
type ObjectResult struct {
	StorageKey string
	FileName   string
	FileSize   int64
	Checksum   string
}

type ChunkInitRequest struct {
	FileName  string
	FileSize  int64
	ChunkSize int64
	Checksum  string
	FileHash  string
	Algorithm string
}

type ChunkInitResult struct {
	UploadID      string
	ChunkSize     int64
	TotalChunks   int
	Instant       bool
	StorageKey    string
	FileID        string
	URL           string
	UploadPath    string
	PartURLs      map[int]string
	PartSignTTL   int
	RecommendConc int
}

type ChunkUploadResult struct {
	UploadID      string
	ChunkIndex    int
	UploadedCount int
	TotalChunks   int
}

type ChunkStatusResult struct {
	UploadID       string
	TotalChunks    int
	UploadedChunks []int
	MissingChunks  []int
}

type ResumeRequest struct {
	FileHash  string
	Algorithm string
	FileName  string
	FileSize  int64
}

type ResumeResult struct {
	Instant       bool
	StorageKey    string
	FileID        string
	URL           string
	UploadID      string
	UploadPath    string
	TotalChunks   int
	UploadedParts []int
	MissingParts  []int
}

// ChunkSession is the snapshot used to validate and recover a merge.
type ChunkSession struct {
	UserID        string
	FileName      string
	FileSize      int64
	TotalChunks   int
	UploadedCount int
	Checksum      string
	Algorithm     string
	Merged        bool
	MergeKey      string
	Assemble      bool
	StorageKey    string
}
