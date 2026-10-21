package upload

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/gin-api/internal/platform/config"
	defaultlogging "github.com/gtkit/gin-api/internal/plugin/builtin/logging"
	"github.com/gtkit/gouploader"
)

// plainHashStore 只实现 HashStore，**不**实现 UserScopedHashStore。
//
// 这是本文件用来触发后台错误的开关：gouploader 在 New() 时对这种组合
// 必然产出一条启动期安全建议（多租户越权风险），并经
// WithOnBackgroundError 上报。相比等待后台清理失败之类的偶发事件，
// 启动期建议是同步且确定的，测试无需轮询或等待。
type plainHashStore struct{}

func (plainHashStore) LookupByHash(context.Context, string, string) (*gouploader.HashedFile, error) {
	return nil, nil
}

func (plainHashStore) Register(context.Context, *gouploader.HashedFile) error { return nil }

// newAdvisoryTriggeringUploader 构造一个必然触发启动期建议的上传器。
// extra 追加在本仓默认装配之后，与生产调用路径一致。
func newAdvisoryTriggeringUploader(t *testing.T, extra ...gouploader.Option) *gouploader.Uploader {
	t.Helper()

	opts := append([]gouploader.Option{
		gouploader.WithChunkTempDir(t.TempDir()),
		gouploader.WithHashStore(plainHashStore{}),
	}, extra...)

	u, err := NewLocalUploader(LocalOptions{
		Dir:          t.TempDir(),
		MaxFileSize:  1 << 20,
		AllowedTypes: []string{".txt"},
	}, opts...)
	if err != nil {
		t.Fatalf("NewLocalUploader() = %v, want nil", err)
	}
	t.Cleanup(u.Close)
	return u
}

// TestBackgroundErrorsReachRepoLogger 钉住「默认出口进入本仓日志管道」。
//
// 断言的不是"回调非 nil"（那只能证明装了个什么东西），而是事件确实落进了
// 本仓 logger/v2 配置的输出——这正是升级要修的那个缺口：v1.7.0 时代
// gouploader 的日志经 logger v1 的 development 兜底写 stderr，绕开本仓配置。
//
// 摘掉 uploader.go 里的 WithOnBackgroundError 装配，本测试必然失败。
func TestBackgroundErrorsReachRepoLogger(t *testing.T) {
	// 不并行：要临时替换进程级默认 logger。
	logDir := t.TempDir()
	initUploadTestLogging(t, logDir)

	newAdvisoryTriggeringUploader(t)
	applog.Sync()

	logged := readAllLogs(t, logDir)
	if !strings.Contains(logged, "UserScopedHashStore") {
		t.Fatalf("启动期安全建议未进入本仓日志管道；\n"+
			"默认的 WithOnBackgroundError 出口应把它记下来。\n实际日志内容:\n%s", logged)
	}
	if !strings.Contains(logged, "upload: gouploader background error") {
		t.Errorf("日志缺少本入口的场景前缀，无法在全站日志里定位来源；实际内容:\n%s", logged)
	}
}

// TestCallerCanReplaceBackgroundErrorSink 钉住「装配方可覆盖默认出口」。
//
// 它守护的是本仓的实现决策——默认出口必须排在 extra **之前**。实测反证：
// 把 uploader.go 里的默认装配移到 extra 之后，本测试即失败（装配方的回调
// 收不到事件，被本仓默认值挡掉）。
//
// 该决策依赖 gouploader「后设置覆盖先设置」的 Option 语义，因此本测试同时
// 是那条上游语义的判据：若它变成"仅在未设置时写入"，这里同样会失败。
//
// 注意它对"默认装配是否存在"不敏感（摘掉默认装配后仍通过）——那一面由
// TestBackgroundErrorsReachRepoLogger 覆盖，两者各守一半，缺一不可。
func TestCallerCanReplaceBackgroundErrorSink(t *testing.T) {
	// 不并行：同样要替换默认 logger，以便断言默认出口**没有**收到事件。
	logDir := t.TempDir()
	initUploadTestLogging(t, logDir)

	var captured []string
	newAdvisoryTriggeringUploader(t, gouploader.WithOnBackgroundError(func(err error) {
		captured = append(captured, err.Error())
	}))
	applog.Sync()

	if len(captured) == 0 {
		t.Fatal("装配方注入的回调未收到启动期安全建议")
	}
	if !strings.Contains(strings.Join(captured, "\n"), "UserScopedHashStore") {
		t.Errorf("装配方的回调收到的不是预期事件: %v", captured)
	}

	// 覆盖而非叠加：默认出口不得同时记一份，否则同一事件被记两次，
	// 且装配方无法关掉本入口的默认行为。
	if logged := readAllLogs(t, logDir); strings.Contains(logged, "gouploader background error") {
		t.Errorf("装配方覆盖后默认出口仍在记录，事件被重复上报；日志内容:\n%s", logged)
	}
}

// readAllLogs 读取目录下所有日志文件并拼接。
// 不猜文件名：logger 按 {path}-{level}.log 命名并可能带轮转后缀，
// 逐个读取比硬编码文件名稳。
func readAllLogs(t *testing.T, dir string) string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	var sb strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sb.Write(b)
	}
	return sb.String()
}

type uploadLoggingConfiguration struct{ cfg *config.Config }

func (c uploadLoggingConfiguration) Value() (*config.Config, error) { return c.cfg, nil }
func initUploadTestLogging(t *testing.T, dir string) {
	t.Helper()
	logging := defaultlogging.New(defaultlogging.Options{Configuration: uploadLoggingConfiguration{&config.Config{Log: config.LogConfig{Path: filepath.Join(dir, "upload"), Level: "debug", FileStdout: 1}}}})
	t.Cleanup(func() {
		if err := logging.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := logging.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
}
