package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"syscall"

	"github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/gin-api/internal/module/file/dto"
)

const (
	multipartMemoryBytes   = 256 << 10
	multipartMetadataBytes = 16 << 10
	multipartFieldBytes    = 4 << 10
	multipartMaxParts      = 4
)

var errMultipartMetadata = errors.New("multipart metadata limit exceeded")

type transferForm struct {
	name     string
	size     int64
	checksum string
	chunk    dto.ChunkForm
	payload  uploadSpool
}

func readTransferForm(req *http.Request, limit int64, stagingDir string, chunk bool) (_ *transferForm, err error) {
	mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || len(params["boundary"]) == 0 || len(params["boundary"]) > 70 {
		return nil, application.ErrInvalid
	}
	guard := metadataReader{reader: req.Body, remaining: -1}
	reader := multipart.NewReader(&guard, params["boundary"])
	form := &transferForm{payload: uploadSpool{dir: stagingDir}}
	defer func() {
		if err != nil {
			_ = form.payload.Close()
		}
	}()
	var fileFound bool
	fields := make(map[string]bool, 3)
	metadataLeft := multipartMetadataBytes
	for count := 0; ; count++ {
		guard.remaining = int64(metadataLeft)
		part, readErr := reader.NextPart()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		guard.remaining = -1
		if count == multipartMaxParts {
			return nil, errMultipartMetadata
		}
		for key, values := range part.Header {
			metadataLeft -= len(key)
			for _, value := range values {
				metadataLeft -= len(value)
			}
		}
		if metadataLeft <= 0 {
			return nil, errMultipartMetadata
		}

		name := part.FormName()
		if name == "file" {
			if fileFound || part.FileName() == "" {
				return nil, application.ErrInvalid
			}
			fileFound, form.name = true, part.FileName()
			if err := form.readPayload(part, limit, !chunk); err != nil {
				return nil, err
			}
			continue
		}
		if !chunk || fields[name] || part.FileName() != "" ||
			name != "upload_id" && name != "chunk_index" && name != "checksum" {
			return nil, application.ErrInvalid
		}
		sizeLimit := min(metadataLeft, multipartFieldBytes)
		value, err := io.ReadAll(io.LimitReader(part, int64(sizeLimit+1)))
		if err != nil {
			return nil, err
		}
		if len(value) > sizeLimit {
			return nil, errMultipartMetadata
		}
		metadataLeft -= len(value)
		fields[name] = true
		switch name {
		case "upload_id":
			form.chunk.UploadID = string(value)
		case "checksum":
			form.chunk.Checksum = string(value)
		case "chunk_index":
			if len(value) != 0 {
				index, err := strconv.Atoi(string(value))
				if err != nil || index < 0 {
					return nil, application.ErrInvalid
				}
				form.chunk.ChunkIndex = index
			}
		}
	}
	if !fileFound || chunk && form.chunk.UploadID == "" {
		return nil, application.ErrInvalid
	}
	// The reader may leave a multipart epilogue. Finish the bounded request
	// body before accepting a chunked upload, so its total cap still applies.
	if _, err := io.Copy(io.Discard, req.Body); err != nil {
		return nil, err
	}
	return form, nil
}

func (f *transferForm) readPayload(part io.Reader, limit int64, checksum bool) error {
	var target io.Writer = &f.payload
	h := sha256.New()
	if checksum {
		target = io.MultiWriter(target, h)
	}
	n, err := io.Copy(target, io.LimitReader(part, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return &http.MaxBytesError{Limit: limit}
	}
	f.size = n
	if checksum {
		f.checksum = hex.EncodeToString(h.Sum(nil))
	}
	return nil
}

// Limit the allocation performed by multipart.NextPart while parsing headers,
// as well as the decoded metadata checked by the caller.
type metadataReader struct {
	reader    io.Reader
	remaining int64
}

func (r *metadataReader) Read(p []byte) (int, error) {
	if r.remaining < 0 {
		return r.reader.Read(p)
	}
	if r.remaining == 0 {
		return 0, errMultipartMetadata
	}
	n, err := r.reader.Read(p[:min(int64(len(p)), r.remaining)])
	r.remaining -= int64(n)
	return n, err
}

type uploadSpool struct {
	dir    string
	memory []byte
	file   *os.File
	path   string
}

func (s *uploadSpool) Write(p []byte) (int, error) {
	if s.file != nil {
		n, err := s.file.Write(p)
		return n, spoolError(err)
	}
	needed := len(s.memory) + len(p)
	if needed <= multipartMemoryBytes {
		if needed > cap(s.memory) {
			buf := make([]byte, len(s.memory), min(multipartMemoryBytes, max(needed, cap(s.memory)*2)))
			copy(buf, s.memory)
			s.memory = buf
		}
		s.memory = append(s.memory, p...)
		return len(p), nil
	}
	file, err := os.CreateTemp(s.dir, "file-spool-")
	if err != nil {
		return 0, spoolError(err)
	}
	s.file, s.path = file, file.Name()
	if runtime.GOOS != "windows" {
		// Unlink before writing data. The kernel frees the spill even when the
		// process dies before cleanup can run, and another replica cannot open it.
		if err := os.Remove(s.path); err != nil {
			return 0, spoolError(err)
		}
		s.path = ""
	}
	if _, err := s.file.Write(s.memory); err != nil {
		return 0, spoolError(err)
	}
	s.memory = nil
	n, err := s.file.Write(p)
	return n, spoolError(err)
}

func (s *uploadSpool) Reader() (io.ReadSeeker, error) {
	if s.file == nil {
		return bytes.NewReader(s.memory), nil
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return nil, spoolError(err)
	}
	return s.file, nil
}

func (s *uploadSpool) Close() error {
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	if s.path != "" {
		err = errors.Join(err, os.Remove(s.path))
	}
	return err
}

func spoolError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return fmt.Errorf("upload staging capacity: %w: %w", application.ErrDependencyUnavailable, err)
	}
	return fmt.Errorf("upload staging failed: %w: %w", application.ErrDependencyUnavailable, err)
}
