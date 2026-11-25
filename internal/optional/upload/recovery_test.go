package upload

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/gtkit/gouploader"
)

type trackedChunk struct {
	io.Reader
	active *int
}

func (c trackedChunk) Close() error {
	*c.active--
	return nil
}

func TestLazyChunkReadersBoundOpenHandlesDuringMerge(t *testing.T) {
	t.Parallel()
	active, peak := 0, 0
	var readers []io.Reader
	for range 1024 {
		readers = append(readers, &lazyChunk{open: func() (io.ReadCloser, error) {
			active++
			peak = max(peak, active)
			return trackedChunk{Reader: strings.NewReader("x"), active: &active}, nil
		}})
	}
	if active != 0 {
		t.Fatal("obtaining chunk readers opened files")
	}
	if n, err := io.Copy(io.Discard, io.MultiReader(readers...)); err != nil || n != 1024 || peak != 1 || active != 0 {
		t.Fatalf("merge: size=%d active=%d peak=%d err=%v", n, active, peak, err)
	}
}

type recordedSave struct {
	gouploader.Storage
	saves int
}

func (s *recordedSave) Save(context.Context, string, io.Reader, int64) error {
	s.saves++
	return nil
}

func TestRegisteredStorageStopsWriteWhenJournalFails(t *testing.T) {
	t.Parallel()
	backend := &recordedSave{}
	store := registeredStorage{Storage: backend}
	failure := errors.New("database write failed")
	ctx := WithWriteRegistration(t.Context(), func(_ context.Context, key string) error {
		if key != "selected-object" || backend.saves != 0 {
			t.Fatalf("registration key=%q saves=%d", key, backend.saves)
		}
		return failure
	})
	if err := store.Save(ctx, "selected-object", strings.NewReader("x"), 1); !errors.Is(err, failure) || backend.saves != 0 {
		t.Fatalf("save=%d after failed intent, err=%v", backend.saves, err)
	}
}
