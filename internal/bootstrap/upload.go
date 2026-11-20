//go:build optional

package bootstrap

import (
	"context"
	"fmt"

	"github.com/gtkit/gin-api/internal/optional/upload"
)

// buildUploader 装配统一上传器：存储驱动按配置单活选择，分片会话落
// upload.redis_db 指定的逻辑库。Uploader.Close 级联关闭注入的会话存储；
// Redis 客户端归 resources 所有，不在此关闭。
func buildUploader(inf *infra, cleanup *providerCleanup) error {
	cfg := inf.cfg.Upload
	uploader, err := upload.NewUploader(upload.UploaderOptions{
		Storage:      cfg.Storage,
		Dir:          cfg.Dir,
		ChunkDir:     cfg.ChunkDir,
		MaxFileSize:  cfg.MaxFileSize,
		AllowedTypes: cfg.AllowedTypes,
		Redis:        inf.resources.Rdb(cfg.RedisDB),
		KeyPrefix:    inf.cfg.EffectiveRedisPrefix(),
	})
	if err != nil {
		return fmt.Errorf("build uploader: %w", err)
	}
	inf.uploader = uploader
	cleanup.push("uploader", cleanupExtensions, func(context.Context) error {
		uploader.Close()
		return nil
	})
	return nil
}
