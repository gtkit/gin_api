package config

import (
	"path/filepath"
	"testing"
)

func TestJWTCommandEnvironmentOverrides(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	writeJWTCommandConfig(t, root, "jwt:\n  active:\n    kid: yaml-kid\n    private_key_path: yaml-private.pem\n    public_key_path: yaml-public.pem\n")
	t.Setenv("APP_JWT__ACTIVE__PRIVATE_KEY_PATH", "override/private.pem")
	t.Setenv("APP_JWT__ACTIVE__PUBLIC_KEY_PATH", "override/public.pem")
	t.Setenv("APP_JWT__ACTIVE__KID", "environment-kid")
	keys, err := LoadJWTKeyConfigForCommand()
	if err != nil {
		t.Fatal(err)
	}
	if keys.KID != "environment-kid" || keys.PrivateKeyPath != filepath.Join(root, "override/private.pem") || keys.PublicKeyPath != filepath.Join(root, "override/public.pem") {
		t.Fatal("existing APP_ precedence not preserved")
	}
}
