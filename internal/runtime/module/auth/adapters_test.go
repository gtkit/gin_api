package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"gorm.io/gorm"

	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/pkg/credential"
	"github.com/gtkit/gin-api/internal/pkg/tokenstore"
	"github.com/gtkit/gin-api/internal/platform/config"
)

// 适配器的全部职责是错误翻译，而翻译错了没有任何编译期迹象：
// 一条漏翻的错误会让"手机号已注册"变成 500、让"令牌过期"变成"服务故障"。
// 因此这里逐条钉住映射。

// TestPasswordHasherTranslatesMismatch 钉住密码错误的翻译。
//
// 漏翻的话 application 收到的是 credential 包的错误，它的 errors.Is
// 判断不成立，一次普通的密码输错会被当成服务端故障返回 500。
func TestPasswordHasherTranslatesMismatch(t *testing.T) {
	t.Parallel()

	// cost 用 bcrypt 的最低值：本测试关心翻译，不关心哈希强度。
	h := passwordHasher{hasher: credential.NewHasher(4)}

	hashed, err := h.Hash("correct")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	if err := h.Compare(hashed, "correct"); err != nil {
		t.Fatalf("Compare with the right password: %v", err)
	}

	err = h.Compare(hashed, "wrong")
	if !errors.Is(err, authapp.ErrInvalidCredentials) {
		t.Fatalf("Compare with a wrong password = %v, want authapp.ErrInvalidCredentials", err)
	}
}

// TestPasswordHasherTranslatesTooLong 钉住超长口令的翻译。
//
// 不翻译的话它会落到 application 的 default 分支变成 500，
// 而这是一个用户输入问题，应当回 422 让用户知道要改什么。
func TestPasswordHasherTranslatesTooLong(t *testing.T) {
	t.Parallel()

	h := passwordHasher{hasher: credential.NewHasher(4)}

	long := make([]byte, 100)
	for i := range long {
		long[i] = 'a'
	}

	_, err := h.Hash(string(long))
	if !errors.Is(err, authapp.ErrPasswordTooLong) {
		t.Fatalf("Hash of an over-long password = %v, want authapp.ErrPasswordTooLong", err)
	}
}

// TestCompareWithDummyTranslatesToInvalidCredentials 钉住时间掩护路径的翻译。
func TestCompareWithDummyTranslatesToInvalidCredentials(t *testing.T) {
	t.Parallel()

	h := passwordHasher{hasher: credential.NewHasher(4)}
	if err := h.CompareWithDummy("anything"); !errors.Is(err, authapp.ErrInvalidCredentials) {
		t.Fatalf("CompareWithDummy = %v, want authapp.ErrInvalidCredentials "+
			"(it must be indistinguishable from a wrong password)", err)
	}
}

// scriptedClient 是可编排返回值的 Redis 替身。
type scriptedClient struct {
	evalVal any
	evalErr error
	setErr  error
}

func (c scriptedClient) ScriptLoad(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult("sha", nil)
}

func (c scriptedClient) EvalSha(context.Context, string, []string, ...any) *redis.Cmd {
	return redis.NewCmdResult(c.evalVal, c.evalErr)
}

func (c scriptedClient) HSet(context.Context, string, ...any) *redis.IntCmd {
	return redis.NewIntResult(1, nil)
}

func (c scriptedClient) Expire(context.Context, string, time.Duration) *redis.BoolCmd {
	return redis.NewBoolResult(true, nil)
}

func (c scriptedClient) Set(context.Context, string, any, time.Duration) *redis.StatusCmd {
	return redis.NewStatusResult("OK", c.setErr)
}

// TestRefreshStoreTranslatesEveryRejection 钉住刷新错误的翻译。
//
// 重用必须与普通拒绝**分开翻译**：两者对客户端同义（都该重新登录），
// 但对运维完全不同——重用意味着令牌被复制了出去，需要告警。
// 合并翻译的话，那条告警永远不会触发。
func TestRefreshStoreTranslatesEveryRejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// scriptCode 是消费脚本的返回码：1 不存在、2 重用、3 族已撤销。
		scriptCode int64
		want       error
	}{
		{name: "not found", scriptCode: 1, want: authapp.ErrRefreshRejected},
		{name: "reused", scriptCode: 2, want: authapp.ErrRefreshReused},
		{name: "family revoked", scriptCode: 3, want: authapp.ErrRefreshRejected},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// 三元组：脚本返回 {code, uid, replaySecret}，第三项是重放窗口内
			// 要原样返回的秘密段（本用例的几种拒绝码都不带它）。
			client := scriptedClient{evalVal: []any{tt.scriptCode, "7", ""}}
			store, err := tokenstore.NewRefreshStore(context.Background(), client, "p", time.Hour)
			if err != nil {
				t.Fatalf("NewRefreshStore: %v", err)
			}

			_, _, err = refreshStore{store: store}.Consume(context.Background(), "fam.secret")
			if !errors.Is(err, tt.want) {
				t.Fatalf("Consume = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestMalformedRefreshTokenIsARejection 钉住畸形令牌不落到 500。
func TestMalformedRefreshTokenIsARejection(t *testing.T) {
	t.Parallel()

	store, err := tokenstore.NewRefreshStore(context.Background(),
		scriptedClient{}, "p", time.Hour)
	if err != nil {
		t.Fatalf("NewRefreshStore: %v", err)
	}

	_, _, err = refreshStore{store: store}.Consume(context.Background(), "no-separator")
	if !errors.Is(err, authapp.ErrRefreshRejected) {
		t.Fatalf("Consume of a malformed token = %v, want authapp.ErrRefreshRejected", err)
	}
}

// TestInfrastructureFailureIsNotTranslated 钉住基础设施故障保持原样。
//
// 把它翻译成"令牌无效"，一次 Redis 抖动会让所有在线用户收到"请重新登录"——
// 而重新登录同样会失败，因为 Redis 还是不可用。
func TestInfrastructureFailureIsNotTranslated(t *testing.T) {
	t.Parallel()

	client := scriptedClient{evalErr: errors.New("connection refused")}
	store, err := tokenstore.NewRefreshStore(context.Background(), client, "p", time.Hour)
	if err != nil {
		t.Fatalf("NewRefreshStore: %v", err)
	}

	_, _, err = refreshStore{store: store}.Consume(context.Background(), "fam.secret")
	if err == nil {
		t.Fatal("an unreachable Redis was reported as success")
	}
	if errors.Is(err, authapp.ErrRefreshRejected) || errors.Is(err, authapp.ErrRefreshReused) {
		t.Fatalf("infrastructure failure was translated into a token rejection: %v", err)
	}
}

// TestLogoutToleratesAMalformedRefreshToken 钉住登出的宽容策略在本层。
//
// 存储层如实报告畸形令牌（它无法定位到任何族），而登出选择容忍：
// 访问令牌那半已经撤销了，一个畸形的刷新令牌本来就不对应任何族，
// 报错只会让用户以为登出失败并反复重试。
func TestLogoutToleratesAMalformedRefreshToken(t *testing.T) {
	t.Parallel()

	store, err := tokenstore.NewRefreshStore(context.Background(),
		scriptedClient{}, "p", time.Hour)
	if err != nil {
		t.Fatalf("NewRefreshStore: %v", err)
	}

	if err := (refreshStore{store: store}).RevokeFamilyOfToken(
		context.Background(), "no-separator"); err != nil {
		t.Fatalf("logging out with a malformed refresh token failed: %v", err)
	}
}

// TestNewRefusesEveryNonLocalMode 是外部身份模式的负向证明。
//
// "不注册认证端点"这句话必须由代码兑现：装配函数在非 local 模式下报错，
// 而不是默默产出一个模块——后者会让一个配置错误变成"生产环境上多了一组
// 没人知道的凭据端点"。
func TestNewRefusesEveryNonLocalMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode config.AuthMode
	}{
		{name: "external", mode: config.AuthModeExternal},
		{name: "empty", mode: ""},
		{name: "unknown", mode: "oauth"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(context.Background(), Deps{Config: config.AuthConfig{Mode: tt.mode}})
			if err == nil {
				t.Fatalf("assembly succeeded with mode %q: the endpoints would exist in a "+
					"deployment that never asked for them", tt.mode)
			}
		})
	}
}

// TestNewRequiresEveryDependency 钉住装配期的依赖检查。
//
// 配置校验已经拦过一次缺 Redis 的组合，这里是第二道：装配代码不该依赖
// "别处一定校验过"——那个前提一旦被改动打破，这里会静默构造出一个无法撤销
// 令牌的认证服务，而登出会看起来正常返回。
func TestNewRequiresEveryDependency(t *testing.T) {
	t.Parallel()

	local := config.AuthConfig{Mode: config.AuthModeLocal}

	tests := []struct {
		name string
		deps Deps
	}{
		{name: "no database", deps: Deps{Config: local}},
		{
			// 一个非 nil 的 gorm 句柄就够了：本用例要走到 Redis 那道检查，
			// 而不是真的访问数据库。
			name: "no redis",
			deps: Deps{Config: local, DB: &gorm.DB{}},
		},
		{
			name: "no access ttl",
			deps: Deps{
				Config: local, DB: &gorm.DB{}, Redis: &redis.Client{},
				Issuer: stubIssuer{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := New(context.Background(), tt.deps); err == nil {
				t.Fatal("assembly succeeded with a missing dependency")
			}
		})
	}
}

// stubIssuer 是最小的签发器替身。
type stubIssuer struct{}

func (stubIssuer) Issue(context.Context, int64, ...string) (string, error) {
	return "access", nil
}
