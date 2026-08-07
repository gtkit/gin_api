package httpclient

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func testConfig() config.OutboundConfig {
	return config.OutboundConfig{
		Defaults: config.OutboundClientConfig{Timeout: 3 * time.Second},
		Clients: map[string]config.OutboundClientConfig{
			"billing":   {BaseURL: "https://billing.example.test"},
			"inventory": {BaseURL: "https://inventory.example.test", Timeout: 8 * time.Second},
		},
	}
}

// TestSameNameReusesOneClient 钉住同名复用的是同一个实例。
//
// 这是本包存在的理由：每次调用新建客户端等于每次新建连接池，连接无法复用，
// 高频调用下本地端口会被 TIME_WAIT 占满。
func TestSameNameReusesOneClient(t *testing.T) {
	t.Parallel()

	factory, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = factory.Close() }()

	first, err := factory.Client("billing")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	second, err := factory.Client("billing")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if first != second {
		t.Fatal("同名两次取到了不同实例：连接池没有被复用")
	}
}

// TestEachNameGetsItsOwnPool 钉住不同名字是不同实例。
//
// 共用一个客户端意味着共用一套连接配额：一个下游的慢响应会把另一个下游的
// 连接一起耗光，故障域就此串在一起。
func TestEachNameGetsItsOwnPool(t *testing.T) {
	t.Parallel()

	factory, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = factory.Close() }()

	billing, err := factory.Client("billing")
	if err != nil {
		t.Fatalf("Client(billing): %v", err)
	}
	inventory, err := factory.Client("inventory")
	if err != nil {
		t.Fatalf("Client(inventory): %v", err)
	}
	if billing == inventory {
		t.Fatal("两个下游共用了同一个客户端：故障域被串在一起")
	}
	if billing.HTTPClient() == inventory.HTTPClient() {
		t.Fatal("两个下游共用了同一个 http.Client：连接配额没有隔离")
	}
}

// TestUnknownNameIsAnError 钉住未登记的名字报错而不是隐式新建。
//
// 按运行期传入的名字隐式建客户端，会让工厂随请求内容无界增长，
// 而一个拼错的名字会悄悄得到一个全新的、谁也没配过的连接池。
func TestUnknownNameIsAnError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     config.OutboundConfig
		lookup  string
		wantErr error
	}{
		{name: "拼错的名字", cfg: testConfig(), lookup: "biling", wantErr: ErrUnknownClient},
		{name: "空配置", cfg: config.OutboundConfig{}, lookup: "billing", wantErr: ErrUnknownClient},
		{name: "空名字", cfg: testConfig(), lookup: "", wantErr: ErrUnknownClient},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			factory, err := New(tt.cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer func() { _ = factory.Close() }()

			client, err := factory.Client(tt.lookup)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Client(%q) error = %v, want %v", tt.lookup, err, tt.wantErr)
			}
			if client != nil {
				t.Fatal("取用失败时仍返回了客户端")
			}
		})
	}
}

// TestNamesAreSortedAndComplete 钉住登记表可读：错误信息里要列出可用的名字。
func TestNamesAreSortedAndComplete(t *testing.T) {
	t.Parallel()

	factory, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = factory.Close() }()

	want := []string{"billing", "inventory"}
	if got := factory.Names(); !slices.Equal(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

// TestMissingBaseURLIsRejected 钉住缺 base_url 时构造失败。
//
// 没有它，"这个名字对应哪个下游"只存在于调用点的字符串里，换环境时无处可改。
func TestMissingBaseURLIsRejected(t *testing.T) {
	t.Parallel()

	_, err := New(config.OutboundConfig{
		Clients: map[string]config.OutboundClientConfig{"billing": {Timeout: time.Second}},
	})
	if err == nil {
		t.Fatal("缺少 base_url 时构造仍然成功了")
	}
}

// TestNilAndRepeatedCloseAreHarmless 钉住收尾路径的健壮性：
// 关停时多处 defer 收尾很常见，重复关闭不得出事；未构造成功时也可能拿到 nil。
func TestNilAndRepeatedCloseAreHarmless(t *testing.T) {
	t.Parallel()

	var nilFactory *Factory
	if err := nilFactory.Close(); err != nil {
		t.Fatalf("nil factory Close: %v", err)
	}
	if _, err := nilFactory.Client("billing"); !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("nil factory Client error = %v, want ErrUnknownClient", err)
	}
	if got := nilFactory.Names(); got != nil {
		t.Fatalf("nil factory Names() = %v, want nil", got)
	}

	factory, err := New(testConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := factory.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := factory.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
