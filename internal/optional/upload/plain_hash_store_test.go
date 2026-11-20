package upload

import (
	"context"

	"github.com/gtkit/gouploader"
)

// plainHashStore is a test-only HashStore that intentionally lacks
// UserScopedHashStore so the uploader emits its startup advisory.
type plainHashStore struct{}

func (plainHashStore) LookupByHash(context.Context, string, string) (*gouploader.HashedFile, error) {
	return nil, nil
}

func (plainHashStore) Register(context.Context, *gouploader.HashedFile) error { return nil }
