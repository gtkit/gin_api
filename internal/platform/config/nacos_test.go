package config

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// writeNacosBody 向假服务端响应写入 body，把写错误上报给测试
// 以满足 errcheck 门禁（check-blank 同样不接受丢弃返回值）。
func writeNacosBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("write nacos fake response: %v", err)
	}
}

// newNacosFakeServer 起一个实现 Nacos 3.x v3 client HTTP API 最小面的假服务端。
// loginErr 为 nil 时登录接口返回固定 token；configHandler 决定配置接口的行为。
func newNacosFakeServer(t *testing.T, loginStatus int, configHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(nacosLoginPath, func(w http.ResponseWriter, r *http.Request) {
		if loginStatus != http.StatusOK {
			w.WriteHeader(loginStatus)
			writeNacosBody(t, w, "login failed")
			return
		}
		writeNacosBody(t, w, `{"code":0,"message":"success","data":{"accessToken":"tok-1"}}`)
	})
	mux.HandleFunc(nacosConfigPath, configHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// fakeServerAddr 返回假服务端的 host:port 形态地址，与 NACOS_ADDR 的取值同构。
func fakeServerAddr(server *httptest.Server) string {
	return strings.TrimPrefix(server.URL, "http://")
}

// writeMinimalConfig 把可通过校验的最小配置写到 root 下的标准配置路径。
func writeMinimalConfig(t *testing.T, root string) {
	t.Helper()
	writeJWTCommandConfig(t, root, minimalConfigYAML())
}

// TestLoadNacosSettingsDisabled 钉住启用开关：NACOS_ADDR 未设置或空白时不启用，
// 也不触发任何网络请求。
func TestLoadNacosSettingsDisabled(t *testing.T) {
	for _, value := range []string{"", "   "} {
		t.Setenv(nacosAddrEnv, value)
		s, err := loadNacosSettings()
		if err != nil {
			t.Fatalf("loadNacosSettings(%q): %v", value, err)
		}
		if s != nil {
			t.Fatalf("loadNacosSettings(%q) = %+v, want nil", value, s)
		}
	}
}

// TestLoadNacosSettingsDefaults 钉住默认值与多地址解析。
func TestLoadNacosSettingsDefaults(t *testing.T) {
	t.Setenv(nacosAddrEnv, "127.0.0.1:8848, 127.0.0.2:8848 ,10.0.0.3:8848")
	t.Setenv(nacosNamespaceEnv, " dev-ns ")
	t.Setenv(nacosDataIDEnv, "")
	t.Setenv(nacosGroupEnv, "")

	s, err := loadNacosSettings()
	if err != nil {
		t.Fatalf("loadNacosSettings: %v", err)
	}
	wantAddrs := []string{"127.0.0.1:8848", "127.0.0.2:8848", "10.0.0.3:8848"}
	if fmt.Sprint(s.addrs) != fmt.Sprint(wantAddrs) {
		t.Fatalf("addrs = %v, want %v", s.addrs, wantAddrs)
	}
	if s.dataID != nacosDefaultDataID {
		t.Fatalf("dataID = %q, want default %q", s.dataID, nacosDefaultDataID)
	}
	if s.group != nacosDefaultGroup {
		t.Fatalf("group = %q, want default %q", s.group, nacosDefaultGroup)
	}
	if s.namespaceID != "dev-ns" {
		t.Fatalf("namespaceID = %q, want %q", s.namespaceID, "dev-ns")
	}
}

// TestLoadNacosSettingsAddrErrors 钉住地址解析的失败形态：空项整体报错。
func TestLoadNacosSettingsAddrErrors(t *testing.T) {
	t.Setenv(nacosAddrEnv, "127.0.0.1:8848,,127.0.0.2:8848")
	if _, err := loadNacosSettings(); err == nil || !strings.Contains(err.Error(), "empty address") {
		t.Fatalf("err = %v, want empty address error", err)
	}
}

// TestLoadNacosSettingsCredentialsPair 钉住凭据成对提供。
func TestLoadNacosSettingsCredentialsPair(t *testing.T) {
	t.Setenv(nacosAddrEnv, "127.0.0.1:8848")
	t.Setenv(nacosUsernameEnv, "alice")

	if _, err := loadNacosSettings(); err == nil ||
		!strings.Contains(err.Error(), "must be provided together") {
		t.Fatalf("err = %v, want credentials pair error", err)
	}
}

// TestApplyNacosOverridesDisabled 钉住 NACOS_ADDR 未设置时 Load 与纯文件加载一致。
func TestApplyNacosOverridesDisabled(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	writeMinimalConfig(t, root)
	t.Setenv("APP_ROOT", root)
	t.Setenv(nacosAddrEnv, "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Database.Password != "" {
		t.Fatalf("database.password = %q, want empty (file value)", cfg.Database.Password)
	}
}

// TestApplyNacosOverridesMerge 钉住三层优先级：本地文件 < Nacos < APP_ 环境变量。
func TestApplyNacosOverridesMerge(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	writeMinimalConfig(t, root)
	t.Setenv("APP_ROOT", root)

	server := newNacosFakeServer(t, http.StatusOK, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("dataId"); got != nacosDefaultDataID {
			t.Errorf("dataId = %q, want %q", got, nacosDefaultDataID)
		}
		if got := r.URL.Query().Get("groupName"); got != nacosDefaultGroup {
			t.Errorf("groupName = %q, want %q", got, nacosDefaultGroup)
		}
		// application.port 在本地文件里是 8008，此处覆盖为 9001；
		// database.password 本地为空，由环境变量最终覆盖。
		writeNacosBody(t, w, `{"code":0,"message":"success","data":"`+
			`application:\n  port: \"9001\"\ndatabase:\n  password: from-nacos\n"}`)
	})
	t.Setenv(nacosAddrEnv, fakeServerAddr(server))
	t.Setenv(nacosUsernameEnv, "alice")
	t.Setenv(nacosPasswordEnv, "secret-pass")
	t.Setenv("APP_DATABASE__PASSWORD", "from-env")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.Port != "9001" {
		t.Fatalf("application.port = %q, want 9001 (nacos beats file)", cfg.App.Port)
	}
	if cfg.Database.Password != "from-env" {
		t.Fatalf("database.password = %q, want from-env (env beats nacos)", cfg.Database.Password)
	}
}

// TestApplyNacosOverridesWrappedContent 钉住 data 为 {content: ...} 对象的响应形态。
func TestApplyNacosOverridesWrappedContent(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	writeMinimalConfig(t, root)
	t.Setenv("APP_ROOT", root)

	server := newNacosFakeServer(t, http.StatusOK, func(w http.ResponseWriter, r *http.Request) {
		writeNacosBody(t, w, `{"code":0,"message":"success","data":{"content":"application:\n  port: \"9002\"\n"}}`)
	})
	t.Setenv(nacosAddrEnv, fakeServerAddr(server))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.Port != "9002" {
		t.Fatalf("application.port = %q, want 9002 (wrapped content)", cfg.App.Port)
	}
}

// TestApplyNacosOverridesAnonymousFetch 钉住未配凭据时匿名拉取可用。
func TestApplyNacosOverridesAnonymousFetch(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	writeMinimalConfig(t, root)
	t.Setenv("APP_ROOT", root)

	server := newNacosFakeServer(t, http.StatusOK, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("accessToken") != "" {
			t.Errorf("anonymous fetch carried a token")
		}
		writeNacosBody(t, w, `{"code":0,"message":"success","data":"application:\n  port: \"9003\"\n"}`)
	})
	t.Setenv(nacosAddrEnv, fakeServerAddr(server))

	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// TestApplyNacosOverridesFailures 钉住启用后的 fail-fast 语义与错误脱敏。
func TestApplyNacosOverridesFailures(t *testing.T) {
	tests := []struct {
		name          string
		loginStatus   int
		credentials   bool
		configHandler func(http.HandlerFunc) http.HandlerFunc
		wantErr       string
		forbidInErr   []string
	}{
		{
			name:        "登录失败",
			loginStatus: http.StatusUnauthorized,
			credentials: true,
			wantErr:     "login failed: HTTP 401",
			forbidInErr: []string{"secret-pass"},
		},
		{
			name: "配置不存在",
			configHandler: func(next http.HandlerFunc) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					writeNacosBody(t, w, `{"code":30002,"message":"config data not exist","data":null}`)
				}
			},
			wantErr: "config fetch rejected: code 30002",
		},
		{
			name: "服务端开启鉴权但未提供凭据",
			configHandler: func(next http.HandlerFunc) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusForbidden)
					writeNacosBody(t, w, `{"code":10001,"message":"access denied","data":""}`)
				}
			},
			wantErr: "set NACOS_USERNAME and NACOS_PASSWORD",
		},
		{
			name: "Nacos 返回非法 YAML",
			configHandler: func(next http.HandlerFunc) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					writeNacosBody(t, w, `{"code":0,"message":"success","data":"application:\n  port: [broken\n"}`)
				}
			},
			wantErr: "parse nacos config",
		},
		{
			name: "响应缺少配置内容",
			configHandler: func(next http.HandlerFunc) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					writeNacosBody(t, w, `{"code":0,"message":"success","data":""}`)
				}
			},
			wantErr: "carries no content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			root := t.TempDir()
			writeMinimalConfig(t, root)
			t.Setenv("APP_ROOT", root)

			defaultConfig := func(w http.ResponseWriter, r *http.Request) {
				writeNacosBody(t, w, `{"code":0,"message":"success","data":"env: dev\n"}`)
			}
			handler := defaultConfig
			if tt.configHandler != nil {
				handler = tt.configHandler(defaultConfig)
			}
			server := newNacosFakeServer(t, tt.loginStatus, handler)
			t.Setenv(nacosAddrEnv, fakeServerAddr(server))
			if tt.credentials {
				t.Setenv(nacosUsernameEnv, "alice")
				t.Setenv(nacosPasswordEnv, "secret-pass")
			}

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
			for _, secret := range tt.forbidInErr {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("err = %v, must not contain %q", err, secret)
				}
			}
		})
	}
}

// TestApplyNacosOverridesTimeout 钉住请求超时使启动失败，而不是无限等待。
func TestApplyNacosOverridesTimeout(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	writeMinimalConfig(t, root)
	t.Setenv("APP_ROOT", root)

	server := newNacosFakeServer(t, http.StatusOK, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		writeNacosBody(t, w, `{"code":0,"message":"success","data":"env: dev\n"}`)
	})
	t.Setenv(nacosAddrEnv, fakeServerAddr(server))

	origClient := nacosHTTPClient
	nacosHTTPClient = &http.Client{Timeout: 50 * time.Millisecond}
	t.Cleanup(func() { nacosHTTPClient = origClient })

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "Timeout") {
		t.Fatalf("err = %v, want a timeout error", err)
	}
}

// TestApplyNacosOverridesMultiAddrFailover 钉住多地址依次尝试：
// 首地址不可达时继续第二个地址并成功。
func TestApplyNacosOverridesMultiAddrFailover(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	writeMinimalConfig(t, root)
	t.Setenv("APP_ROOT", root)

	good := newNacosFakeServer(t, http.StatusOK, func(w http.ResponseWriter, r *http.Request) {
		writeNacosBody(t, w, `{"code":0,"message":"success","data":"application:\n  port: \"9004\"\n"}`)
	})
	t.Setenv(nacosAddrEnv, "127.0.0.1:1,"+fakeServerAddr(good))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.App.Port != "9004" {
		t.Fatalf("application.port = %q, want 9004 (failover address)", cfg.App.Port)
	}
}

// TestApplyNacosOverridesAllAddrFailed 钉住全部地址失败的错误聚合。
func TestApplyNacosOverridesAllAddrFailed(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	writeMinimalConfig(t, root)
	t.Setenv("APP_ROOT", root)
	t.Setenv(nacosAddrEnv, "127.0.0.1:1,127.0.0.1:2")

	origClient := nacosHTTPClient
	nacosHTTPClient = &http.Client{Timeout: 200 * time.Millisecond}
	t.Cleanup(func() { nacosHTTPClient = origClient })

	_, err := Load()
	if err == nil {
		t.Fatalf("err = nil, want all-address failure")
	}
	for _, addr := range []string{"127.0.0.1:1", "127.0.0.1:2"} {
		if !strings.Contains(err.Error(), addr) {
			t.Fatalf("err = %v, want it to mention address %q", err, addr)
		}
	}
}

// TestApplyNacosOverridesConfigFileMissing 钉住本地文件仍是必读打底来源：
// 文件缺失时即使 Nacos 可用也按既有语义报错。
func TestApplyNacosOverridesConfigFileMissing(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_ROOT", t.TempDir())
	t.Setenv(nacosAddrEnv, "127.0.0.1:8848")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "read config file") {
		t.Fatalf("err = %v, want config file read error", err)
	}
}
