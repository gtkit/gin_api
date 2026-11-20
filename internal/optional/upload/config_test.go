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
		{"explicit cloud", DriverS3, DriverS3},
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
	}
	tests := []struct {
		name    string
		mutate  func(*StorageConfig)
		wantErr bool
	}{
		{"empty driver is local", func(c *StorageConfig) { c.Driver = "" }, false},
		{"unknown driver", func(c *StorageConfig) { c.Driver = "gcs" }, true},
		{"removed cloud driver", func(c *StorageConfig) { c.Driver = "aliyun" }, true},
		{"complete s3", func(c *StorageConfig) { c.Driver = DriverS3 }, false},
		{"s3 missing secret", func(c *StorageConfig) { c.Driver = DriverS3; c.S3.SecretKey = " " }, true},
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
