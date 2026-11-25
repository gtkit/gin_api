package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gtkit/gouploader"
	"github.com/gtkit/gouploader/chunkstore"
	gouploaderredis "github.com/gtkit/gouploader/redis"
	goredis "github.com/redis/go-redis/v9"
)

// RecoverySessions uses the uploader-owned Redis session store. The binding
// expires with its session and deliberately excludes storage credentials.
type RecoverySessions struct {
	*gouploaderredis.Store
	client          *goredis.Client
	prefix          string
	namespace       string
	requireAssemble bool
}

func NewRecoveryUploader(o UploaderOptions) (*gouploader.Uploader, *RecoverySessions, error) {
	var location string
	if o.Storage.EffectiveDriver() == DriverLocal {
		path, err := filepath.Abs(o.Dir)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve local upload storage: %w", err)
		}
		location = "local\x00" + path
	} else {
		s3 := o.Storage.S3
		location = "s3\x00" + s3.Endpoint + "\x00" + s3.Bucket + "\x00" + s3.Region + "\x00" + strconv.FormatBool(s3.UseSSL)
	}
	hash := sha256.Sum256([]byte(location))
	sessions := &RecoverySessions{client: o.Redis, prefix: o.KeyPrefix + "file-location:", namespace: hex.EncodeToString(hash[:]),
		requireAssemble: o.Storage.EffectiveDriver() == DriverLocal}
	up, err := newUploader(o, sessions)
	return up, sessions, err
}

func (s *RecoverySessions) Namespace() string { return s.namespace }

func (s *RecoverySessions) CreateSession(ctx context.Context, session *chunkstore.Session) error {
	if s.requireAssemble && !session.Assemble {
		return errors.New("local upload could not prepare an assembly file")
	}
	ttl := time.Until(time.Unix(session.ExpiresAt, 0))
	if ttl <= 0 {
		return chunkstore.ErrSessionExpired
	}
	if err := s.client.Set(ctx, s.prefix+session.UploadID, s.binding(session), ttl).Err(); err != nil {
		return fmt.Errorf("bind upload session to storage: %w", err)
	}
	return s.Store.CreateSession(ctx, session)
}

// OpenChunkData postpones opening a proxy chunk until the SDK consumes it.
// Its merge implementation obtains readers for the entire session first.
func (s *RecoverySessions) OpenChunkData(ctx context.Context, id string, index int) (io.ReadCloser, error) {
	return &lazyChunk{open: func() (io.ReadCloser, error) {
		return s.Store.OpenChunkData(ctx, id, index)
	}}, nil
}

type lazyChunk struct {
	open   func() (io.ReadCloser, error)
	reader io.ReadCloser
	closed bool
}

func (c *lazyChunk) Read(p []byte) (int, error) {
	if c.closed {
		return 0, io.EOF
	}
	if c.reader == nil {
		reader, err := c.open()
		if err != nil {
			c.closed = true
			return 0, err
		}
		c.reader = reader
	}
	n, err := c.reader.Read(p)
	if err != nil {
		if closeErr := c.Close(); closeErr != nil {
			return n, errors.Join(err, closeErr)
		}
	}
	return n, err
}

func (c *lazyChunk) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if c.reader == nil {
		return nil
	}
	return c.reader.Close()
}

func (s *RecoverySessions) binding(session *chunkstore.Session) string {
	return s.namespace + ":" + session.UserID
}

func (s *RecoverySessions) Lookup(ctx context.Context, id string) (*chunkstore.Session, bool, error) {
	session, err := s.GetSession(ctx, id)
	if err != nil {
		return nil, false, err
	}
	location, err := s.client.Get(ctx, s.prefix+id).Result()
	if errors.Is(err, goredis.Nil) {
		return session, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read upload session storage: %w", err)
	}
	return session, location == s.binding(session), nil
}

type registrationKey struct{}

func WithWriteRegistration(ctx context.Context, register func(context.Context, string) error) context.Context {
	return context.WithValue(ctx, registrationKey{}, register)
}

type registeredStorage struct{ gouploader.Storage }

func (s registeredStorage) Save(ctx context.Context, key string, reader io.Reader, size int64) error {
	register, ok := ctx.Value(registrationKey{}).(func(context.Context, string) error)
	if !ok {
		return errors.New("object write lacks file operation registration")
	}
	if err := register(ctx, key); err != nil {
		return err
	}
	return s.Storage.Save(ctx, key, reader, size)
}
