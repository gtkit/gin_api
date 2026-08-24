package upload

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gtkit/gouploader"
	logger "github.com/gtkit/logger/v2"
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
// 上传文件的回源路由务必挂 middleware.UploadedFileHeaders（见本包 Policy）。
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
