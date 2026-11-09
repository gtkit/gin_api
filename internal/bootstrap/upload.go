//go:build optional

package bootstrap

import (
	"context"
	"fmt"

	"github.com/gtkit/gin-api/internal/optional/upload"
	"github.com/gtkit/gouploader"
)

func buildLocalUploader(inf *infra, cleanup *providerCleanup, options upload.LocalOptions, extra ...gouploader.Option) error {
	uploader, err := upload.NewLocalUploader(options, extra...)
	if err != nil {
		return fmt.Errorf("build local uploader: %w", err)
	}
	inf.uploader = uploader
	cleanup.push("local uploader", cleanupExtensions, func(context.Context) error {
		uploader.Close()
		return nil
	})
	return nil
}
