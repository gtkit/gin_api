package upload

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// 存储驱动标识。同一部署只激活一个驱动（单活）；files 记录会带上
// 驱动名与桶名，为将来多云路由或迁移留溯源信息。
const (
	DriverLocal   = "local"
	DriverS3      = "s3"
	DriverAliyun  = "aliyun"
	DriverTencent = "tencent"
	DriverHuawei  = "huawei"
)

// StorageConfig 选择单活存储驱动并提供各驱动的连接参数。
//
// Driver 为空按 local 处理（兼容只有 dir/chunk_dir 的存量配置）。
// 云凭据经 APP_UPLOAD__STORAGE__* 环境变量覆盖注入，不落提交的配置文件。
// 字段与 gouploader 各 adapter 的 Config 一一对应；UseSSL 原样透传，
// endpoint 不带 scheme 时由它决定 HTTP/HTTPS——是否加密是部署方的显式决策。
type StorageConfig struct {
	Driver  string               `mapstructure:"driver"`
	S3      S3StorageConfig      `mapstructure:"s3"`
	Aliyun  AliyunStorageConfig  `mapstructure:"aliyun"`
	Tencent TencentStorageConfig `mapstructure:"tencent"`
	Huawei  HuaweiStorageConfig  `mapstructure:"huawei"`
}

// S3StorageConfig 覆盖 S3 兼容对象存储（MinIO、AWS 等）。
type S3StorageConfig struct {
	Endpoint  string `mapstructure:"endpoint"`
	Bucket    string `mapstructure:"bucket"`
	Region    string `mapstructure:"region"`
	UseSSL    bool   `mapstructure:"use_ssl"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	CDNHost   string `mapstructure:"cdn_host"`
}

// AliyunStorageConfig 对应 gouploader/aliyun adapter。
type AliyunStorageConfig struct {
	Endpoint        string `mapstructure:"endpoint"`
	Bucket          string `mapstructure:"bucket"`
	Region          string `mapstructure:"region"`
	UseSSL          bool   `mapstructure:"use_ssl"`
	AccessKeyID     string `mapstructure:"access_key_id"`
	AccessKeySecret string `mapstructure:"access_key_secret"`
}

// TencentStorageConfig 对应 gouploader/tencent adapter。
type TencentStorageConfig struct {
	BucketURL string `mapstructure:"bucket_url"`
	SecretID  string `mapstructure:"secret_id"`
	SecretKey string `mapstructure:"secret_key"`
}

// HuaweiStorageConfig 对应 gouploader/huawei adapter。
type HuaweiStorageConfig struct {
	Endpoint  string `mapstructure:"endpoint"`
	Bucket    string `mapstructure:"bucket"`
	Region    string `mapstructure:"region"`
	UseSSL    bool   `mapstructure:"use_ssl"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
}

// EffectiveDriver 返回生效驱动：空值按 local。
func (c StorageConfig) EffectiveDriver() string {
	if strings.TrimSpace(c.Driver) == "" {
		return DriverLocal
	}
	return c.Driver
}

// Validate 校验驱动合法性与该驱动的必填连接参数。
// local 的落盘目录由装配入口校验（Dir 字段在 UploadConfig 层）。
func (c StorageConfig) Validate() error {
	switch c.EffectiveDriver() {
	case DriverLocal:
		return nil
	case DriverS3:
		return requireFields("s3", map[string]string{
			"endpoint":   c.S3.Endpoint,
			"bucket":     c.S3.Bucket,
			"access_key": c.S3.AccessKey,
			"secret_key": c.S3.SecretKey,
		})
	case DriverAliyun:
		return requireFields("aliyun", map[string]string{
			"endpoint":          c.Aliyun.Endpoint,
			"bucket":            c.Aliyun.Bucket,
			"access_key_id":     c.Aliyun.AccessKeyID,
			"access_key_secret": c.Aliyun.AccessKeySecret,
		})
	case DriverTencent:
		return requireFields("tencent", map[string]string{
			"bucket_url": c.Tencent.BucketURL,
			"secret_id":  c.Tencent.SecretID,
			"secret_key": c.Tencent.SecretKey,
		})
	case DriverHuawei:
		return requireFields("huawei", map[string]string{
			"endpoint":   c.Huawei.Endpoint,
			"bucket":     c.Huawei.Bucket,
			"access_key": c.Huawei.AccessKey,
			"secret_key": c.Huawei.SecretKey,
		})
	default:
		return fmt.Errorf("upload: unknown storage driver %q (want local/s3/aliyun/tencent/huawei)", c.Driver)
	}
}

// BucketName 返回当前驱动的桶名，用于文件记录溯源。
// local 无桶概念返回空串；tencent 的桶名是 BucketURL 主机名的第一段
// （形如 my-bucket-1250000000.cos.ap-guangzhou.myqcloud.com）。
func (c StorageConfig) BucketName() string {
	switch c.EffectiveDriver() {
	case DriverS3:
		return c.S3.Bucket
	case DriverAliyun:
		return c.Aliyun.Bucket
	case DriverHuawei:
		return c.Huawei.Bucket
	case DriverTencent:
		u, err := url.Parse(c.Tencent.BucketURL)
		if err != nil || u.Host == "" {
			return ""
		}
		bucket, _, _ := strings.Cut(u.Host, ".")
		return bucket
	default:
		return ""
	}
}

func requireFields(driver string, fields map[string]string) error {
	var missing []string
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if strings.TrimSpace(fields[name]) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("upload: storage.%s requires %s", driver, strings.Join(missing, ", "))
	}
	return nil
}
