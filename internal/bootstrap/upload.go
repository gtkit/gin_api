package bootstrap

// UPLOAD: 可选上传器的构造与关闭接线。
// 启用前按 internal/optional/upload/README.md 设置存储目录和文件策略，再取消下面注释。
// wiring:begin upload.impl upload block
/*
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
*/
// wiring:end upload.impl
