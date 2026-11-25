package upload

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gtkit/gouploader"
	gouploaderredis "github.com/gtkit/gouploader/redis"
	"github.com/gtkit/logger/v2"
	goredis "github.com/redis/go-redis/v9"
)

// LocalOptions 是本地磁盘上传器的必填参数。
//
// 三项全部必填、无默认值：上传是"宽松默认最危险"的能力——gouploader
// 自身的默认是 10GB 上限、类型不限（空白名单在库语义里是全部放行），
// 那正是"无上限上传 + 任意可执行文件落盘"的攻击面。母版的纪律是
// 安全决策不设默认值，由装配方显式写下。
type LocalOptions struct {
	// Dir 是文件落盘根目录。
	Dir string
	// MaxFileSize 是单文件字节上限，必须为正——无上限等于把磁盘交给客户端。
	MaxFileSize int64
	// AllowedTypes 是允许的扩展名白名单（如 ".jpg" ".png"），必须非空。
	AllowedTypes []string
}

// NewLocalUploader 构造本地磁盘上传器，是 gouploader 的仓内装配入口
// （依赖版本由母版钉住并随门禁验证）。
//
// 返回的 *gouploader.Uploader 持有后台清理 goroutine，进程关停时由
// 调用方 Close。附加能力（分片参数、回调、公开 URL 构造等）经 extra
// 透传；对象存储与 STS 直传直接用 gouploader 原生 API 构造即可。
// 上传文件的回源路由务必挂 FileHeaders（见本包 Policy）。
//
// 后台错误默认有出口：gouploader 自 v1.8.0 起不再自行输出日志，无返回路径
// 的错误（后台会话清理失败、被隔离的钩子 panic、宽松模式降级放行、启动期
// 安全建议）只经 WithOnBackgroundError 上报，未装配时**丢弃**。因此本入口
// 默认把它接进本仓日志管道，装配方无需额外接线。
//
// 级别取 Error 而非 Warn：该回调收到的是混合内容——清理失败与钩子 panic
// 属 Error，降级放行与启动建议更接近 Warn，而上游未提供级别区分的入参。
// 取 Error 是因为这些事件没有第二条上报路径，漏看的代价大于误报的代价。
//
// 该默认出口排在 extra 之前，因此装配方经 extra 传入自己的
// WithOnBackgroundError 即可取代它（gouploader 的 Option 语义为后设置
// 覆盖先设置）。
func NewLocalUploader(o LocalOptions, extra ...gouploader.Option) (*gouploader.Uploader, error) {
	if strings.TrimSpace(o.Dir) == "" {
		return nil, errors.New("upload: local dir is required")
	}
	if o.MaxFileSize <= 0 {
		return nil, errors.New("upload: max file size must be positive: an unbounded upload hands the disk to clients")
	}
	if len(o.AllowedTypes) == 0 {
		return nil, errors.New("upload: allowed types must be explicit: an empty whitelist means gouploader accepts every type")
	}

	opts := append([]gouploader.Option{
		gouploader.WithLocalStorage(o.Dir),
		gouploader.WithMaxFileSize(o.MaxFileSize),
		gouploader.WithAllowedTypes(o.AllowedTypes...),
		// 排在 extra 之前：装配方可用自己的实现取代（见函数注释）。
		gouploader.WithOnBackgroundError(func(err error) {
			logger.Errorf("upload: gouploader background error: %v", err)
		}),
	}, extra...)
	uploader, err := gouploader.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("upload: assemble local uploader: %w", err)
	}
	return uploader, nil
}

// UploaderOptions 是统一装配入口的必填参数。
//
// 安全三项（大小上限、类型白名单、分片临时目录）对所有驱动必填，
// local 驱动另要求落盘目录；Redis 客户端承载分片会话与续传索引，
// 缺失即构造失败——没有它会静默退回进程内文件会话，重启即丢进度。
type UploaderOptions struct {
	// Storage 选择单活存储驱动及连接参数。
	Storage StorageConfig
	// Dir 是 local 驱动的落盘根目录。
	Dir string
	// ChunkDir 是代理路径分片数据的本地目录（所有驱动必填）。
	ChunkDir string
	// MaxFileSize 是单文件字节上限，必须为正。
	MaxFileSize int64
	// AllowedTypes 是允许的扩展名白名单，必须非空。
	AllowedTypes []string
	// Redis 是分片会话存储客户端，归调用方所有（Close 不关闭它）。
	Redis *goredis.Client
	// KeyPrefix 是项目 Redis 命名空间；会话键在其下追加 "gu:"。
	KeyPrefix string
}

// NewUploader 构造统一上传器：按 StorageConfig 选择存储驱动，
// 分片会话落 Redis（跨进程/多实例续传），并强制会话身份策略。
//
// 与 NewLocalUploader 的关系：后者是纯本地、文件会话的存量入口，保持不变；
// 业务路由装配用本入口。v1 全走代理路径（WithMultipartDisabled），
// 云驱动仅经 Storage 四方法读写；STS/预签名直传后续另行接入。
//
// 身份策略：WithUserIDFn 从请求 context 的 UploadInfo 解析请求者，
// WithRequireUserID 禁止匿名共享秒传索引；会话创建者的硬校验由
// application 层在调用前注入身份完成（库不拒绝匿名 init）。
//
// 后台错误默认出口与 NewLocalUploader 同款，排在 extra 之前可被覆盖；
// Redis 会话存储的后台错误（孤儿数据清理等）同样接入本仓日志。
// 返回的 Uploader.Close 会级联关闭注入的会话存储。
func NewUploader(o UploaderOptions, extra ...gouploader.Option) (*gouploader.Uploader, error) {
	return newUploader(o, nil, extra...)
}

func newUploader(o UploaderOptions, recovery *RecoverySessions, extra ...gouploader.Option) (*gouploader.Uploader, error) {
	if strings.TrimSpace(o.ChunkDir) == "" {
		return nil, errors.New("upload: chunk dir is required")
	}
	if o.MaxFileSize <= 0 {
		return nil, errors.New("upload: max file size must be positive: an unbounded upload hands the disk to clients")
	}
	if len(o.AllowedTypes) == 0 {
		return nil, errors.New("upload: allowed types must be explicit: an empty whitelist means gouploader accepts every type")
	}
	if err := o.Storage.Validate(); err != nil {
		return nil, err
	}
	if o.Redis == nil {
		return nil, errors.New("upload: redis client is required for chunk sessions")
	}
	if o.Storage.EffectiveDriver() == DriverLocal && strings.TrimSpace(o.Dir) == "" {
		return nil, errors.New("upload: local dir is required")
	}

	storageOpt, err := storageOption(o)
	if err != nil {
		return nil, err
	}

	chunkStore, err := gouploaderredis.New(o.Redis,
		gouploaderredis.WithKeyPrefix(o.KeyPrefix+"gu:"),
		gouploaderredis.WithDataDir(o.ChunkDir),
		gouploaderredis.WithOnBackgroundError(func(err error) {
			logger.Errorf("upload: chunk store background error: %v", err)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("upload: assemble redis chunk store: %w", err)
	}

	opts := []gouploader.Option{
		storageOpt,
		gouploader.WithMaxFileSize(o.MaxFileSize),
		gouploader.WithAllowedTypes(o.AllowedTypes...),
		gouploader.WithChunkStore(chunkStore),
		gouploader.WithUserIDFn(userIDFromUploadInfo),
		gouploader.WithRequireUserID(),
		gouploader.WithMultipartDisabled(),
		// 排在 extra 之前：装配方可用自己的实现取代。
		gouploader.WithOnBackgroundError(func(err error) {
			logger.Errorf("upload: gouploader background error: %v", err)
		}),
	}
	var storage gouploader.Storage
	if recovery != nil {
		recovery.Store = chunkStore
		opts = append(opts, gouploader.WithChunkStore(recovery))
		if o.Storage.EffectiveDriver() == DriverS3 {
			opts = append(opts, gouploader.WithStorageResolver(gouploader.StorageResolverFunc(
				func(context.Context, gouploader.ObjectScope) (gouploader.Storage, error) {
					return registeredStorage{Storage: storage}, nil
				},
			)))
		}
	}
	if o.Storage.EffectiveDriver() == DriverLocal {
		// 本地代理合并走预分配装配文件：会话过期/中断后的 *.assembling
		// 残留由 sweeper 周期回收。云驱动不产生装配残留（分片数据在
		// chunk_dir，由 Redis 会话存储的孤儿清理回收），不启用以免启动噪音。
		opts = append(opts, gouploader.WithOrphanCleanup(24*time.Hour, time.Hour))
	}
	opts = append(opts, extra...)
	uploader, err := gouploader.New(opts...)
	if err != nil {
		// 构造失败时会话存储的后台清理协程已启动，就地关闭避免泄漏。
		_ = chunkStore.Close()
		return nil, fmt.Errorf("upload: assemble uploader: %w", err)
	}
	storage = uploader.Storage()
	return uploader, nil
}

// storageOption 把驱动配置翻译成 gouploader 的存储选项。
// 调用前 StorageConfig.Validate 已通过，default 分支不可达。
func storageOption(o UploaderOptions) (gouploader.Option, error) {
	switch o.Storage.EffectiveDriver() {
	case DriverLocal:
		return gouploader.WithLocalStorage(o.Dir), nil
	case DriverS3:
		s := o.Storage.S3
		return gouploader.WithS3Storage(gouploader.S3Config{
			Endpoint:  s.Endpoint,
			AccessKey: s.AccessKey,
			SecretKey: s.SecretKey,
			Bucket:    s.Bucket,
			Region:    s.Region,
			UseSSL:    s.UseSSL,
			CDNHost:   s.CDNHost,
		}), nil
	default:
		return nil, fmt.Errorf("upload: unknown storage driver %q", o.Storage.Driver)
	}
}

// userIDFromUploadInfo 是 WithUserIDFn 的实现：runtime 适配器在每次
// 引擎调用前经 ContextWithUploadInfo 注入请求者身份。
func userIDFromUploadInfo(ctx context.Context) string {
	if info, ok := gouploader.UploadInfoFromContext(ctx); ok {
		return info.UserID
	}
	return ""
}
