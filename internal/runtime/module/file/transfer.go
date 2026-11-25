package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/gin-api/internal/optional/upload"
	"github.com/gtkit/gouploader"
)

// NewTransfer adapts the borrowed uploader to application file and chunk I/O.
func NewTransfer(up *gouploader.Uploader) application.Transfer {
	if up == nil {
		return nil
	}
	return uploaderTransfer{up: up}
}

type uploaderTransfer struct{ up *gouploader.Uploader }

func uploadContext(ctx context.Context, owner int64) context.Context {
	return gouploader.ContextWithUploadInfo(ctx, gouploader.UploadInfo{UserID: strconv.FormatInt(owner, 10)})
}

func (a uploaderTransfer) Upload(ctx context.Context, owner int64, filename string, r io.Reader, size int64) (*application.ObjectResult, error) {
	result, err := a.up.UploadService().Upload(uploadContext(ctx, owner), filename, r, size, "", "")
	return objectResult(result), transferError(err)
}

func (a uploaderTransfer) ChunkInit(ctx context.Context, owner int64, req *application.ChunkInitRequest) (*application.ChunkInitResult, error) {
	request := gouploader.ChunkInitRequest(*req)
	result, err := a.up.ChunkService().InitUpload(uploadContext(ctx, owner), &request)
	return (*application.ChunkInitResult)(result), transferError(err)
}

func (a uploaderTransfer) UploadChunk(ctx context.Context, owner int64, id string, index int, r io.Reader, size int64, checksum string) (*application.ChunkUploadResult, error) {
	result, err := a.up.ChunkService().UploadChunk(uploadContext(ctx, owner), id, index, r, size, checksum)
	return (*application.ChunkUploadResult)(result), transferError(err)
}

func (a uploaderTransfer) Status(ctx context.Context, owner int64, id string) (*application.ChunkStatusResult, error) {
	result, err := a.up.ChunkService().GetStatus(uploadContext(ctx, owner), id)
	return (*application.ChunkStatusResult)(result), transferError(err)
}

func (a uploaderTransfer) Resume(ctx context.Context, owner int64, req *application.ResumeRequest) (*application.ResumeResult, error) {
	result, err := a.up.ChunkService().Resume(uploadContext(ctx, owner), (*gouploader.ResumeRequest)(req))
	return (*application.ResumeResult)(result), transferError(err)
}

func (a uploaderTransfer) Abort(ctx context.Context, owner int64, id string) error {
	return transferError(a.up.ChunkService().Abort(uploadContext(ctx, owner), id))
}

func (a uploaderTransfer) Merge(ctx context.Context, owner int64, id string, register func(context.Context, string) error) (*application.ObjectResult, error) {
	ctx = uploadContext(ctx, owner)
	if register != nil {
		ctx = upload.WithWriteRegistration(ctx, register)
	}
	result, err := a.up.ChunkService().Merge(ctx, id)
	return objectResult(result), transferError(err)
}

func (a uploaderTransfer) Save(ctx context.Context, owner int64, key string, r io.Reader, size int64) error {
	return transferError(a.up.Storage().Save(uploadContext(ctx, owner), key, r, size))
}

func (a uploaderTransfer) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	result, err := a.up.Storage().Get(ctx, key)
	return result, transferError(err)
}

func (a uploaderTransfer) Delete(ctx context.Context, key string) error {
	return transferError(a.up.Storage().Delete(ctx, key))
}

func (a uploaderTransfer) Exists(ctx context.Context, key string) (bool, error) {
	result, err := a.up.Storage().Exists(ctx, key)
	return result, transferError(err)
}

func objectResult(result *gouploader.UploadResult) *application.ObjectResult {
	if result == nil {
		return nil
	}
	return &application.ObjectResult{
		StorageKey: result.StorageKey,
		FileName:   result.FileName,
		FileSize:   result.FileSize,
		Checksum:   result.Checksum,
	}
}

func recoverySessionLookup(sessions *upload.RecoverySessions) func(context.Context, string) (application.ChunkSession, bool, error) {
	return func(ctx context.Context, id string) (application.ChunkSession, bool, error) {
		session, bound, err := sessions.Lookup(ctx, id)
		if err != nil {
			return application.ChunkSession{}, false, transferError(err)
		}
		return application.ChunkSession{
			UserID: session.UserID, FileName: session.FileName, FileSize: session.FileSize,
			TotalChunks: session.TotalChunks, UploadedCount: len(session.Uploaded), Checksum: session.Checksum,
			Algorithm: session.Algorithm, Merged: session.Merged, MergeKey: session.MergeKey,
			Assemble: session.Assemble, StorageKey: session.StorageKey,
		}, bound, nil
	}
}

var transferErrors = [...]struct{ sdk, domain error }{
	{gouploader.ErrSessionNotFound, application.ErrSessionNotFound},
	{gouploader.ErrSessionExpired, application.ErrSessionExpired},
	{gouploader.ErrUnauthorized, application.ErrSessionOwner},
	{gouploader.ErrFileTypeNotAllow, application.ErrFileTypeNotAllowed},
	{gouploader.ErrFileTooLarge, application.ErrFileTooLarge},
	{gouploader.ErrChecksumMismatch, application.ErrChecksumMismatch},
	{gouploader.ErrChunkHashMismatch, application.ErrChecksumMismatch},
	{gouploader.ErrChunkIndexOutRange, application.ErrInvalid},
	{gouploader.ErrMergeIncomplete, application.ErrMergeIncomplete},
	{gouploader.ErrMergeInProgress, application.ErrMergeInProgress},
	{gouploader.ErrInvalidParam, application.ErrInvalid},
	{gouploader.ErrStorageWrite, application.ErrStorageUnavailable},
	{gouploader.ErrStorageRead, application.ErrStorageUnavailable},
	{gouploader.ErrStorageDelete, application.ErrStorageUnavailable},
}

func transferError(err error) error {
	if err == nil {
		return nil
	}
	// Match in the HTTP mapper's established precedence.
	for _, match := range transferErrors {
		if errors.Is(err, match.sdk) {
			return fmt.Errorf("%w: %w", match.domain, err)
		}
	}
	return err
}
