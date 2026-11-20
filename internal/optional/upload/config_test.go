package upload

import "testing"

func TestStorageConfigEffectiveDriver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		driver string
		want   string
	}{
		{"empty defaults to local", "", DriverLocal},
		{"blank defaults to local", "  ", DriverLocal},
		{"explicit local", DriverLocal, DriverLocal},
		{"explicit cloud", DriverAliyun, DriverAliyun},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := (StorageConfig{Driver: tt.driver}).EffectiveDriver(); got != tt.want {
				t.Fatalf("EffectiveDriver() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStorageConfigValidate(t *testing.T) {
	t.Parallel()

	complete := StorageConfig{
		Driver: DriverS3,
		S3:     S3StorageConfig{Endpoint: "minio:9000", Bucket: "b", AccessKey: "ak", SecretKey: "sk"},
		Aliyun: AliyunStorageConfig{Endpoint: "oss-cn-hangzhou.aliyuncs.com", Bucket: "b", AccessKeyID: "ak", AccessKeySecret: "sk"},
		Tencent: TencentStorageConfig{
			BucketURL: "https://b-1250000000.cos.ap-guangzhou.myqcloud.com",
			SecretID:  "id", SecretKey: "sk",
		},
		Huawei: HuaweiStorageConfig{Endpoint: "obs.cn-north-4.myhuaweicloud.com", Bucket: "b", AccessKey: "ak", SecretKey: "sk"},
	}
	tests := []struct {
		name    string
		mutate  func(*StorageConfig)
		wantErr bool
	}{
		{"empty driver is local", func(c *StorageConfig) { c.Driver = "" }, false},
		{"unknown driver", func(c *StorageConfig) { c.Driver = "gcs" }, true},
		{"complete s3", func(c *StorageConfig) { c.Driver = DriverS3 }, false},
		{"s3 missing secret", func(c *StorageConfig) { c.Driver = DriverS3; c.S3.SecretKey = " " }, true},
		{"complete aliyun", func(c *StorageConfig) { c.Driver = DriverAliyun }, false},
		{"aliyun missing bucket", func(c *StorageConfig) { c.Driver = DriverAliyun; c.Aliyun.Bucket = "" }, true},
		{"complete tencent", func(c *StorageConfig) { c.Driver = DriverTencent }, false},
		{"tencent missing bucket url", func(c *StorageConfig) { c.Driver = DriverTencent; c.Tencent.BucketURL = "" }, true},
		{"complete huawei", func(c *StorageConfig) { c.Driver = DriverHuawei }, false},
		{"huawei missing endpoint", func(c *StorageConfig) { c.Driver = DriverHuawei; c.Huawei.Endpoint = "" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := complete
			tt.mutate(&c)
			if err := c.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStorageConfigBucketName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  StorageConfig
		want string
	}{
		{"local has no bucket", StorageConfig{}, ""},
		{"s3", StorageConfig{Driver: DriverS3, S3: S3StorageConfig{Bucket: "files"}}, "files"},
		{"aliyun", StorageConfig{Driver: DriverAliyun, Aliyun: AliyunStorageConfig{Bucket: "files"}}, "files"},
		{"huawei", StorageConfig{Driver: DriverHuawei, Huawei: HuaweiStorageConfig{Bucket: "files"}}, "files"},
		{
			"tencent first host label",
			StorageConfig{Driver: DriverTencent, Tencent: TencentStorageConfig{BucketURL: "https://files-1250000000.cos.ap-guangzhou.myqcloud.com"}},
			"files-1250000000",
		},
		{"tencent invalid url", StorageConfig{Driver: DriverTencent, Tencent: TencentStorageConfig{BucketURL: "://bad"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.cfg.BucketName(); got != tt.want {
				t.Fatalf("BucketName() = %q, want %q", got, tt.want)
			}
		})
	}
}
