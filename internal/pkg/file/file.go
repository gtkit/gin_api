package file

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gtkit/stringx"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"
)

type Runtime struct {
	uploadBaseDir func() string
	timeNow       func() time.Time
	timezone      func() *time.Location
}

var defaultRuntime = NewRuntime(nil)

func NewRuntime(cfg *config.Config) *Runtime {
	runtime := &Runtime{
		uploadBaseDir: func() string { return resource.ResolvePath("public/uploads") },
		timeNow:       time.Now,
		timezone:      func() *time.Location { return time.Local },
	}

	if cfg != nil {
		runtime.timezone = func() *time.Location {
			location, err := time.LoadLocation(cfg.App.Timezone)
			if err != nil || location == nil {
				return time.Local
			}
			return location
		}
	}

	return runtime
}

func InitWithConfig(cfg *config.Config) {
	runtime := NewRuntime(cfg)
	if defaultRuntime != nil {
		if defaultRuntime.uploadBaseDir != nil {
			runtime.uploadBaseDir = defaultRuntime.uploadBaseDir
		}
		if defaultRuntime.timeNow != nil {
			runtime.timeNow = defaultRuntime.timeNow
		}
	}

	defaultRuntime = runtime
}

func Put(data []byte, to string) error { return os.WriteFile(to, data, 0o600) }

func Exists(fileToCheck string) bool {
	if _, err := os.Stat(fileToCheck); os.IsNotExist(err) {
		return false
	}
	return true
}

func DirExists(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err == nil {
		if fi.IsDir() {
			return true, nil
		}
		return false, errors.New("存在同名文件")
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func FileNameWithoutExtension(fileName string) string {
	return strings.TrimSuffix(fileName, filepath.Ext(fileName))
}

func Position(file *os.File) (int64, error) {
	if file == nil {
		return 0, errors.New("null fd when retrieving file position")
	}
	return file.Seek(0, io.SeekCurrent)
}

func FilePathFromUploadFile(filetype string) string {
	fileDir := stringx.BuilderJoin([]string{defaultRuntime.uploadBaseDir(), "/", filetype, "/", TimenowInTimezone().Format(time.DateOnly)})
	_ = CreateDirIfNotExists(fileDir, 0o755)
	return fileDir
}

func RandomNameFromUploadFile(file *multipart.FileHeader) string {
	return stringx.Random(16) + filepath.Ext(file.Filename)
}

func TimenowInTimezone() time.Time {
	return defaultRuntime.timeNow().In(defaultRuntime.timezone())
}

func CreateDirIfNotExists(dirname string, perm ...os.FileMode) error {
	if _, err := os.Stat(dirname); err != nil {
		if os.IsNotExist(err) {
			if len(perm) == 0 {
				perm = []os.FileMode{os.ModePerm}
			}
			return os.MkdirAll(dirname, perm[0])
		}
		return err
	}
	return nil
}

func Del(filePath string) error { return os.RemoveAll(filePath) }

func Move(src string, dst string) (err error) {
	if dst == "" {
		return nil
	}
	src, err = filepath.Abs(src)
	if err != nil {
		return err
	}
	dst, err = filepath.Abs(dst)
	if err != nil {
		return err
	}
	retry := false
	dir := filepath.Dir(dst)
redo:
	_, err = os.Stat(dir)
	if err != nil {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if !retry {
			retry = true
			goto redo
		}
	}
	return os.Rename(src, dst)
}

func GenerateSafeFileName(original string) string {
	ext := filepath.Ext(original)
	name := strings.TrimSuffix(original, ext)
	hash := sha256.Sum256([]byte(name + defaultRuntime.timeNow().String()))
	return fmt.Sprintf("%x%s", hash, ext)
}

func IsPathValid(storagePath, path string) bool {
	storageRoot, err := filepath.Abs(filepath.Clean(storagePath))
	if err != nil {
		return false
	}
	targetPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	relPath, err := filepath.Rel(storageRoot, targetPath)
	if err != nil {
		return false
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		return false
	}
	return isSafeRelativePath(relPath)
}

func ValidateFileType(file io.ReadSeeker) error {
	mimeType, err := detectContentType(file)
	if err != nil {
		return err
	}
	if !allowedMimeTypes[mimeType] {
		return fmt.Errorf("不允许的文件类型: %s", mimeType)
	}
	return nil
}

func detectContentType(file io.ReadSeeker) (string, error) {
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read file header: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("reset file pointer: %w", err)
	}
	contentType := http.DetectContentType(buf[:n])
	if mediaType, _, parseErr := mime.ParseMediaType(contentType); parseErr == nil {
		contentType = mediaType
	}
	return contentType, nil
}

func isSafeRelativePath(relPath string) bool {
	if relPath == "." {
		return true
	}
	for _, part := range strings.Split(relPath, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.ContainsAny(part, "~$&*|<>") || isReservedPathName(part) {
			return false
		}
	}
	return true
}

func isReservedPathName(part string) bool {
	base := strings.TrimSuffix(part, filepath.Ext(part))
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

func resetFileHooksForTest() {
	defaultRuntime = NewRuntime(nil)
}

func setTimeNowForTest(fn func() time.Time) { defaultRuntime.timeNow = fn }
