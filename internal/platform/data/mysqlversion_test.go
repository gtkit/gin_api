package dao

import "testing"

func TestParseServerVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		version    string
		comment    string
		wantMajor  int
		wantMinor  int
		wantPatch  int
		wantVendor mysqlVendor
		wantErr    bool
	}{
		{
			name: "oracle mysql", version: "8.0.36", comment: "MySQL Community Server - GPL",
			wantMajor: 8, wantMinor: 0, wantPatch: 36, wantVendor: vendorOracleMySQL,
		},
		{
			name: "oracle mysql with distro suffix", version: "8.0.36-0ubuntu0.22.04.1", comment: "(Ubuntu)",
			wantMajor: 8, wantMinor: 0, wantPatch: 36, wantVendor: vendorUnknown,
		},
		{
			name: "percona", version: "8.0.35-27", comment: "Percona Server (GPL), Release 27",
			wantMajor: 8, wantMinor: 0, wantPatch: 35, wantVendor: vendorPercona,
		},
		{
			// 这条是本文件存在的理由：10.6 数值上大于 8.0，只比数字会把 MariaDB 误放行。
			name: "mariadb identified from version string", version: "10.6.16-MariaDB-1:10.6.16+maria~ubu2004",
			comment:   "mariadb.org binary distribution",
			wantMajor: 10, wantMinor: 6, wantPatch: 16, wantVendor: vendorMariaDB,
		},
		{
			name: "mariadb identified from comment only", version: "10.11.6", comment: "MariaDB Server",
			wantMajor: 10, wantMinor: 11, wantPatch: 6, wantVendor: vendorMariaDB,
		},
		{
			name: "two segment version", version: "8.0", comment: "MySQL Community Server - GPL",
			wantMajor: 8, wantMinor: 0, wantPatch: 0, wantVendor: vendorOracleMySQL,
		},
		{
			name: "leading whitespace tolerated", version: "  8.0.36  ", comment: "MySQL",
			wantMajor: 8, wantMinor: 0, wantPatch: 36, wantVendor: vendorOracleMySQL,
		},
		{name: "empty", version: "", comment: "", wantErr: true},
		{name: "garbage", version: "not-a-version", comment: "", wantErr: true},
		{name: "major only", version: "8", comment: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseServerVersion(tt.version, tt.comment)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseServerVersion() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.major != tt.wantMajor || got.minor != tt.wantMinor || got.patch != tt.wantPatch {
				t.Fatalf("version = %d.%d.%d, want %d.%d.%d",
					got.major, got.minor, got.patch, tt.wantMajor, tt.wantMinor, tt.wantPatch)
			}
			if got.vendor != tt.wantVendor {
				t.Fatalf("vendor = %v, want %v", got.vendor, tt.wantVendor)
			}
		})
	}
}

// TestVendorSupported 锁住 vendor 白名单：放开新 vendor 必须是显式决定，
// 不能因为版本号数值大就被放行。
func TestVendorSupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		vendor mysqlVendor
		want   bool
	}{
		{vendorOracleMySQL, true},
		{vendorPercona, true},
		{vendorMariaDB, false},
		{vendorUnknown, false},
	}
	for _, tt := range tests {
		t.Run(tt.vendor.String(), func(t *testing.T) {
			t.Parallel()
			if got := tt.vendor.supported(); got != tt.want {
				t.Fatalf("%v.supported() = %v, want %v", tt.vendor, got, tt.want)
			}
		})
	}
}

func TestServerVersionAtLeast(t *testing.T) {
	t.Parallel()

	v := func(ma, mi, pa int) serverVersion {
		return serverVersion{major: ma, minor: mi, patch: pa}
	}

	tests := []struct {
		name string
		have serverVersion
		want serverVersion
		ok   bool
	}{
		{name: "equal", have: v(8, 0, 36), want: v(8, 0, 36), ok: true},
		{name: "higher patch", have: v(8, 0, 37), want: v(8, 0, 36), ok: true},
		{name: "lower patch", have: v(8, 0, 35), want: v(8, 0, 36), ok: false},
		{name: "higher minor", have: v(8, 1, 0), want: v(8, 0, 36), ok: true},
		{name: "lower minor", have: v(8, 0, 99), want: v(8, 1, 0), ok: false},
		{name: "higher major", have: v(9, 0, 0), want: v(8, 0, 0), ok: true},
		{name: "lower major", have: v(5, 7, 44), want: v(8, 0, 0), ok: false},
		// 两段式最低版本：8.0 意味着 8.0.0，任何 8.0.x 都满足。
		{name: "two segment minimum", have: v(8, 0, 0), want: v(8, 0, 0), ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.have.atLeast(tt.want); got != tt.ok {
				t.Fatalf("atLeast() = %v, want %v", got, tt.ok)
			}
		})
	}
}

func TestHasStrictSQLMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sqlMode string
		want    bool
	}{
		{name: "trans tables", sqlMode: "STRICT_TRANS_TABLES", want: true},
		{name: "all tables", sqlMode: "STRICT_ALL_TABLES", want: true},
		{
			name:    "mysql 8 default",
			sqlMode: "ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ENGINE_SUBSTITUTION",
			want:    true,
		},
		{name: "lowercase tolerated", sqlMode: "strict_trans_tables", want: true},
		{name: "spaces tolerated", sqlMode: "ONLY_FULL_GROUP_BY, STRICT_TRANS_TABLES", want: true},
		{name: "empty", sqlMode: "", want: false},
		{name: "non strict only", sqlMode: "ONLY_FULL_GROUP_BY,NO_ENGINE_SUBSTITUTION", want: false},
		// 子串不算：STRICT_TRANS_TABLES_EXTRA 不是严格模式。
		{name: "substring is not a match", sqlMode: "STRICT_TRANS_TABLES_EXTRA", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := hasStrictSQLMode(tt.sqlMode); got != tt.want {
				t.Fatalf("hasStrictSQLMode(%q) = %v, want %v", tt.sqlMode, got, tt.want)
			}
		})
	}
}

func TestParseMinVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    [3]int
		wantErr bool
	}{
		{name: "two segments", input: "8.0", want: [3]int{8, 0, 0}},
		{name: "three segments", input: "8.0.36", want: [3]int{8, 0, 36}},
		{name: "garbage", input: "abc", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseMinVersion(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseMinVersion() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if [3]int{got.major, got.minor, got.patch} != tt.want {
				t.Fatalf("parseMinVersion() = %v, want %v", [3]int{got.major, got.minor, got.patch}, tt.want)
			}
		})
	}
}
