package config

import "testing"

func TestValidatePprofConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     Env
		pprof   PprofConfig
		wantErr bool
	}{
		{name: "prod non-loopback rejected", env: EnvProd, pprof: PprofConfig{Enabled: true, Host: "0.0.0.0"}, wantErr: true},
		{name: "prod public ip rejected", env: EnvProd, pprof: PprofConfig{Enabled: true, Host: "10.0.0.5"}, wantErr: true},
		{name: "prod loopback ok", env: EnvProd, pprof: PprofConfig{Enabled: true, Host: "127.0.0.1"}, wantErr: false},
		{name: "prod empty host ok", env: EnvProd, pprof: PprofConfig{Enabled: true, Host: ""}, wantErr: false},
		{name: "prod disabled ok", env: EnvProd, pprof: PprofConfig{Enabled: false, Host: "0.0.0.0"}, wantErr: false},
		{name: "dev non-loopback ok", env: Env("dev"), pprof: PprofConfig{Enabled: true, Host: "0.0.0.0"}, wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validatePprofConfig(tc.env, tc.pprof)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validatePprofConfig(%s, %+v) err=%v, wantErr=%v", tc.env, tc.pprof, err, tc.wantErr)
			}
		})
	}
}
