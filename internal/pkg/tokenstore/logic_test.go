package tokenstore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 本文件用**手写替身**覆盖不需要真实 Lua 的那部分逻辑。
//
// 与 refresh_test.go 的 miniredis 版是互补关系，不是重复：
//
//   - miniredis 版真的执行脚本，覆盖原子消费与重用检测——那是替身证明不了的；
//   - 手写替身能精确制造故障（撤销失败、脚本缓存丢失、写入中途报错），
//     那是 miniredis 很难触发的；而这些恰恰是最需要有正确处置的路径。
//
// 另一个实际收益：miniredis 要监听回环端口，受限环境会整包跳过——
// 那时这个包的覆盖率会归零。本文件不碰网络，任何环境都跑得起来。

// fakeRefreshClient 是可编排的 Redis 替身。
type fakeRefreshClient struct {
	mu sync.Mutex

	scriptLoadErr error
	// scriptLoads 记录脚本被加载的次数，用于验证 NOSCRIPT 后确实重载了。
	scriptLoads int

	// evalResults 按调用顺序返回；用完后重复最后一个。
	evalResults []evalResult
	evalCalls   int
	// evalKeys 记录每次调用收到的 keys，用于验证键的构造。
	evalKeys [][]string

	setErr error
	// setKeys 记录撤销标记写到了哪个键。
	setKeys []string
}

type evalResult struct {
	val any
	err error
}

func (c *fakeRefreshClient) ScriptLoad(_ context.Context, _ string) *redis.StringCmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scriptLoads++
	if c.scriptLoadErr != nil {
		return redis.NewStringResult("", c.scriptLoadErr)
	}
	return redis.NewStringResult("sha-1", nil)
}

func (c *fakeRefreshClient) EvalSha(_ context.Context, _ string, keys []string, _ ...any) *redis.Cmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evalKeys = append(c.evalKeys, keys)

	idx := c.evalCalls
	c.evalCalls++
	if idx >= len(c.evalResults) {
		if len(c.evalResults) == 0 {
			return redis.NewCmdResult(nil, errors.New("no result configured"))
		}
		idx = len(c.evalResults) - 1
	}
	r := c.evalResults[idx]
	return redis.NewCmdResult(r.val, r.err)
}

func (c *fakeRefreshClient) Set(_ context.Context, key string, _ any, _ time.Duration) *redis.StatusCmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setKeys = append(c.setKeys, key)
	return redis.NewStatusResult("OK", c.setErr)
}

func newFakeStore(t *testing.T, client *fakeRefreshClient) *RefreshStore {
	t.Helper()
	store, err := NewRefreshStore(context.Background(), client, "test", time.Hour, strings.Repeat("r", 32))
	if err != nil {
		t.Fatalf("NewRefreshStore: %v", err)
	}
	return store
}

// TestNewRefreshStoreValidatesItsInputs 覆盖装配期检查。
func TestNewRefreshStoreValidatesItsInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		client RefreshClient
		ttl    time.Duration
	}{
		{name: "no client", client: nil, ttl: time.Hour},
		{name: "zero ttl", client: &fakeRefreshClient{}, ttl: 0},
		{name: "negative ttl", client: &fakeRefreshClient{}, ttl: -time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewRefreshStore(context.Background(), tt.client, "p", tt.ttl, strings.Repeat("r", 32)); err == nil {
				t.Fatal("assembly succeeded with an invalid input")
			}
		})
	}
}

// TestScriptIsPreloadedAtAssembly 钉住脚本在装配期加载。
//
// 留到第一次刷新才加载的话，一个不可达的 Redis 会表现为"服务起来了，
// 但所有人都登不了录"——那种故障应当在启动时就暴露。
func TestScriptIsPreloadedAtAssembly(t *testing.T) {
	t.Parallel()

	client := &fakeRefreshClient{scriptLoadErr: errors.New("redis unreachable")}
	if _, err := NewRefreshStore(context.Background(), client, "p", time.Hour, strings.Repeat("r", 32)); err == nil {
		t.Fatal("assembly succeeded while the script could not be loaded")
	}
	if client.scriptLoads == 0 {
		t.Error("the script was never loaded at assembly time")
	}
}

// TestKeysShareAClusterHashTag 钉住集群语义。
//
// 消费脚本同时访问令牌记录与族撤销标记。Redis 集群要求一次调用里的所有键
// 落在同一个 slot，否则直接拒绝执行——而那是**只在集群部署上**出现的失败，
// 单机测试永远发现不了。hash tag `{family}` 是保证它们同 slot 的机制。
func TestKeysShareAClusterHashTag(t *testing.T) {
	t.Parallel()

	client := &fakeRefreshClient{evalResults: []evalResult{{val: []any{int64(1), "", ""}}}}
	store := newFakeStore(t, client)

	if _, _, err := store.Consume(context.Background(), "fam1.secret1"); !errors.Is(err, ErrRefreshNotFound) {
		t.Fatalf("absent refresh: %v", err)
	}

	if len(client.evalKeys) != 1 {
		t.Fatalf("eval calls = %d, want 1", len(client.evalKeys))
	}
	keys := client.evalKeys[0]
	if len(keys) != 3 {
		t.Fatalf("keys = %v, want 3 of them", keys)
	}

	tag := hashTagOf(t, keys[0])
	for _, k := range keys[1:] {
		if got := hashTagOf(t, k); got != tag {
			t.Fatalf("keys land in different cluster slots (%q vs %q): a Redis Cluster "+
				"would refuse the script outright, and only in a clustered deployment",
				keys[0], k)
		}
	}
	if tag != "fam1" {
		t.Errorf("hash tag = %q, want the family id", tag)
	}
}

// hashTagOf 取出 Redis 集群 hash tag（第一对花括号之间的内容）。
func hashTagOf(t *testing.T, key string) string {
	t.Helper()
	open := strings.Index(key, "{")
	closeIdx := strings.Index(key, "}")
	if open < 0 || closeIdx < open {
		t.Fatalf("key %q has no cluster hash tag", key)
	}
	return key[open+1 : closeIdx]
}

// TestSecretIsNotUsedRawInKeys 钉住键里不含明文秘密段。
func TestSecretIsNotUsedRawInKeys(t *testing.T) {
	t.Parallel()

	client := &fakeRefreshClient{evalResults: []evalResult{{val: []any{int64(1), "", ""}}}}
	store := newFakeStore(t, client)

	const secret = "distinctive-secret-value"
	if _, _, err := store.Consume(context.Background(), "fam."+secret); !errors.Is(err, ErrRefreshNotFound) {
		t.Fatalf("absent refresh: %v", err)
	}

	for _, k := range client.evalKeys[0] {
		if strings.Contains(k, secret) {
			t.Fatalf("key %q embeds the raw secret", k)
		}
	}
}

// TestNoScriptTriggersReloadAndRetry 钉住 Redis 重启后的自愈。
//
// Redis 重启会清空脚本缓存。不重载就让这次刷新失败，等于一次重启把所有
// 在线用户都踢下线。
func TestNoScriptTriggersReloadAndRetry(t *testing.T) {
	t.Parallel()

	client := &fakeRefreshClient{
		evalResults: []evalResult{
			{err: errors.New("NOSCRIPT No matching script")},
			{val: []any{int64(0), "42", ""}},
		},
	}
	store := newFakeStore(t, client)
	loadsBefore := client.scriptLoads

	uid, next, err := store.Consume(context.Background(), "fam.secret")
	if err != nil {
		t.Fatalf("Consume after NOSCRIPT: %v", err)
	}
	if uid != 42 {
		t.Errorf("uid = %d, want 42", uid)
	}
	if next == "" {
		t.Error("no rotated token returned")
	}
	if client.scriptLoads <= loadsBefore {
		t.Error("the script was not reloaded after NOSCRIPT")
	}
}

// TestReloadFailureIsReported 钉住重载失败不被吞掉。
func TestReloadFailureIsReported(t *testing.T) {
	t.Parallel()

	client := &fakeRefreshClient{
		evalResults: []evalResult{{err: errors.New("NOSCRIPT No matching script")}},
	}
	store := newFakeStore(t, client)
	client.scriptLoadErr = errors.New("redis went away")

	if _, _, err := store.Consume(context.Background(), "fam.secret"); err == nil {
		t.Fatal("Consume succeeded although the script could not be reloaded")
	}
}

// TestReuseVerdictImpliesRevocationAlreadyHappened 钉住重用撤销的原子性
// （local-authentication spec）：返回重用判定意味着族撤销标记**此刻已写入**，
// 因此 Go 侧不再补一次撤销调用——那一次补调用正是"已判定重用而族尚未撤销"
// 窗口的来源，窗口内并发请求可以消费下一代令牌并换出新的访问令牌。
func TestReuseVerdictImpliesRevocationAlreadyHappened(t *testing.T) {
	t.Parallel()

	client := &fakeRefreshClient{
		evalResults: []evalResult{{val: []any{int64(2), "7", ""}}}, // 2 = 重用
	}
	store := newFakeStore(t, client)

	_, _, err := store.Consume(context.Background(), "fam.secret")
	if !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("Consume = %v, want ErrRefreshReused", err)
	}
	// 撤销在脚本内完成：不该再有独立的 Set 调用。
	if len(client.setKeys) != 0 {
		t.Fatalf("a separate revocation call was made (%v): the window it opens is exactly "+
			"what moving revocation into the script removes", client.setKeys)
	}
}

// TestScriptFailureOnReuseIsNotAReuseVerdict 钉住脚本执行失败的处置：
// 撤销与检测同在脚本内，脚本失败意味着**两者都没发生**——必须如实报基础设施
// 错误，不能返回重用判定（那会让调用方以为族已撤销）。
func TestScriptFailureOnReuseIsNotAReuseVerdict(t *testing.T) {
	t.Parallel()

	client := &fakeRefreshClient{
		evalResults: []evalResult{{err: errors.New("connection refused")}},
	}
	store := newFakeStore(t, client)

	_, _, err := store.Consume(context.Background(), "fam.secret")
	if err == nil {
		t.Fatal("an unreachable Redis was reported as success")
	}
	if errors.Is(err, ErrRefreshReused) {
		t.Fatal("a script failure was reported as a reuse verdict: the caller would believe " +
			"the family was revoked while nothing happened")
	}
}

// TestConsumeMapsEveryScriptCode 覆盖脚本返回码到错误的映射。
func TestConsumeMapsEveryScriptCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  any
		want error
	}{
		{name: "not found", val: []any{int64(1), "", ""}, want: ErrRefreshNotFound},
		{name: "reused", val: []any{int64(2), "7", ""}, want: ErrRefreshReused},
		{name: "family revoked", val: []any{int64(3), "7", ""}, want: ErrFamilyRevoked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeStore(t, &fakeRefreshClient{evalResults: []evalResult{{val: tt.val}}})
			_, _, err := store.Consume(context.Background(), "fam.secret")
			if !errors.Is(err, tt.want) {
				t.Fatalf("Consume = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestMalformedScriptResultIsAnError 钉住脚本返回形状变了要报错。
//
// 静默当成某个已知返回码，会让一次脚本改动悄悄改变认证语义。
func TestMalformedScriptResultIsAnError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  any
	}{
		{name: "not a list", val: "ok"},
		{name: "wrong arity", val: []any{int64(0), ""}},
		{name: "non-integer code", val: []any{"zero", "7", ""}},
		{name: "unknown code", val: []any{int64(99), "7", ""}},
		{name: "non-numeric uid", val: []any{int64(0), "not-a-number", ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeStore(t, &fakeRefreshClient{evalResults: []evalResult{{val: tt.val}}})
			if _, _, err := store.Consume(context.Background(), "fam.secret"); err == nil {
				t.Fatalf("a malformed script result %#v was accepted", tt.val)
			}
		})
	}
}

// TestIssueReportsStorageFailures 覆盖签发路径的写入失败与 NOSCRIPT 自愈。
//
// 写入与 TTL 已合并为单个脚本原子执行（issueScript）：失败点只剩脚本
// 调用本身；Redis 重启清空脚本缓存时按 NOSCRIPT 重载后重试。
func TestIssueReportsStorageFailures(t *testing.T) {
	t.Parallel()

	t.Run("script eval fails", func(t *testing.T) {
		t.Parallel()

		store := newFakeStore(t, &fakeRefreshClient{
			evalResults: []evalResult{{err: errors.New("write failed")}},
		})
		if _, err := store.Issue(context.Background(), 1); err == nil {
			t.Fatal("Issue reported success although the record was not stored")
		}
	})

	t.Run("noscript reloads and retries", func(t *testing.T) {
		t.Parallel()

		client := &fakeRefreshClient{
			evalResults: []evalResult{
				{err: errors.New("NOSCRIPT No matching script")},
				{val: int64(1)},
			},
		}
		store := newFakeStore(t, client)
		loadsBefore := client.scriptLoads
		if _, err := store.Issue(context.Background(), 1); err != nil {
			t.Fatalf("Issue after NOSCRIPT: %v", err)
		}
		if client.scriptLoads <= loadsBefore {
			t.Fatal("the issue script was not reloaded after NOSCRIPT")
		}
	})
}

// TestRevokeFamilyRequiresAFamily 覆盖入参校验。
func TestRevokeFamilyRequiresAFamily(t *testing.T) {
	t.Parallel()

	store := newFakeStore(t, &fakeRefreshClient{})
	if err := store.RevokeFamily(context.Background(), ""); err == nil {
		t.Fatal("RevokeFamily accepted an empty family")
	}
}

// TestRevokeFamilyOfMalformedTokenReportsIt 钉住存储层如实报告畸形令牌。
//
// **宽容与否不是存储层的决定**：本层只回答"这个令牌能不能定位到一个族"。
// 登出流程选择容忍它（见 runtime/module/auth 的适配器：访问令牌那半已经
// 撤销了，一个畸形的刷新令牌本来就不对应任何族），但那是策略，
// 属于装配层。存储层若自作主张返回 nil，调用方就再也无法区分
// "没有族可撤"与"撤销成功"了。
func TestRevokeFamilyOfMalformedTokenReportsIt(t *testing.T) {
	t.Parallel()

	store := newFakeStore(t, &fakeRefreshClient{})
	err := store.RevokeFamilyOfToken(context.Background(), "no-separator")
	if !errors.Is(err, ErrMalformedToken) {
		t.Fatalf("RevokeFamilyOfToken = %v, want ErrMalformedToken", err)
	}
}

// TestIsNoScriptMatchesOnlyTheRealThing 钉住 NOSCRIPT 判定不误伤。
//
// 判宽了会把普通错误当成缓存缺失，白白多一次脚本加载；判窄了则让
// Redis 重启后所有刷新都失败。
func TestIsNoScriptMatchesOnlyTheRealThing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil},
		{name: "real noscript", err: errors.New("NOSCRIPT No matching script"), want: true},
		{name: "unrelated error", err: errors.New("connection refused")},
		{name: "mentions noscript later", err: errors.New("failed: NOSCRIPT")},
		{name: "short error", err: errors.New("NO")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isNoScript(tt.err); got != tt.want {
				t.Fatalf("isNoScript(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestSplitToken 覆盖令牌形状解析。
func TestSplitToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		token      string
		wantFamily string
		wantSecret string
		wantErr    bool
	}{
		{name: "well formed", token: "fam.secret", wantFamily: "fam", wantSecret: "secret"},
		{
			// 秘密段本身可以含分隔符：只在第一个处切分。
			name: "separator inside the secret", token: "fam.a.b",
			wantFamily: "fam", wantSecret: "a.b",
		},
		{name: "no separator", token: "famsecret", wantErr: true},
		{name: "empty", token: "", wantErr: true},
		{name: "empty family", token: ".secret", wantErr: true},
		{name: "empty secret", token: "fam.", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			family, secret, err := splitToken(tt.token)
			if tt.wantErr {
				if !errors.Is(err, ErrMalformedToken) {
					t.Fatalf("splitToken(%q) error = %v, want ErrMalformedToken", tt.token, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitToken(%q): %v", tt.token, err)
			}
			if family != tt.wantFamily || secret != tt.wantSecret {
				t.Fatalf("splitToken(%q) = (%q, %q), want (%q, %q)",
					tt.token, family, secret, tt.wantFamily, tt.wantSecret)
			}
		})
	}
}

// TestIssuedTokensAreUnpredictable 钉住令牌不可预测。
//
// 令牌就是凭据本身，可预测的令牌等于没有令牌。
func TestIssuedTokensAreUnpredictable(t *testing.T) {
	t.Parallel()

	store := newFakeStore(t, &fakeRefreshClient{
		evalResults: []evalResult{{val: int64(1)}}, // 签发脚本恒成功；fake 重复最后一个结果
	})
	seen := make(map[string]struct{}, 64)
	for range 64 {
		token, err := store.Issue(context.Background(), 1)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if _, dup := seen[token]; dup {
			t.Fatalf("Issue produced a duplicate token %q", token)
		}
		seen[token] = struct{}{}
	}
}

// --- 撤销表：同样不需要网络 ---

type fakeRevocationClient struct {
	setErr    error
	existsErr error
	exists    int64
	// setTTLs 记录每次写入用的存活时间。
	setTTLs []time.Duration
}

func (c *fakeRevocationClient) Set(_ context.Context, _ string, _ any, ttl time.Duration) *redis.StatusCmd {
	c.setTTLs = append(c.setTTLs, ttl)
	return redis.NewStatusResult("OK", c.setErr)
}

func (c *fakeRevocationClient) Exists(_ context.Context, _ ...string) *redis.IntCmd {
	return redis.NewIntResult(c.exists, c.existsErr)
}

func TestNewRevocationListRequiresAClient(t *testing.T) {
	t.Parallel()

	if _, err := NewRevocationList(nil, "p", 0); err == nil {
		t.Fatal("NewRevocationList accepted a nil client")
	}
}

// TestRevocationTTLTracksTheRemainingLifetime 钉住存活时间跟随令牌寿命。
func TestRevocationTTLTracksTheRemainingLifetime(t *testing.T) {
	t.Parallel()

	client := &fakeRevocationClient{}
	list, err := NewRevocationList(client, "p", 0)
	if err != nil {
		t.Fatalf("NewRevocationList: %v", err)
	}
	// 固定时钟让断言确定：真实时间下 TTL 会带上执行耗时。
	now := time.Unix(1754000000, 0)
	list.now = func() time.Time { return now }

	if err := list.Revoke(context.Background(), "jti", now.Add(10*time.Minute)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if len(client.setTTLs) != 1 || client.setTTLs[0] != 10*time.Minute {
		t.Fatalf("ttls = %v, want a single 10m entry: the record must die with the token",
			client.setTTLs)
	}
}

func TestRevokeRequiresATokenID(t *testing.T) {
	t.Parallel()

	client := &fakeRevocationClient{}
	list, err := NewRevocationList(client, "p", 0)
	if err != nil {
		t.Fatalf("NewRevocationList: %v", err)
	}

	if err := list.Revoke(context.Background(), "", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("Revoke accepted an empty token id")
	}
	if len(client.setTTLs) != 0 {
		t.Error("a record was written for an empty token id")
	}
}

func TestRevokeReportsStorageFailure(t *testing.T) {
	t.Parallel()

	list, err := NewRevocationList(&fakeRevocationClient{setErr: errors.New("down")}, "p", 0)

	if err != nil {
		t.Fatalf("NewRevocationList: %v", err)
	}
	if err := list.Revoke(context.Background(), "jti", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("Revoke reported success although nothing was stored: logout would silently " +
			"do nothing")
	}
}

func TestIsRevokedReadsTheStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		exists int64
		want   bool
	}{
		{name: "absent", exists: 0},
		{name: "present", exists: 1, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			list, err := NewRevocationList(&fakeRevocationClient{exists: tt.exists}, "p", 0)

			if err != nil {
				t.Fatalf("NewRevocationList: %v", err)
			}
			got, err := list.IsRevoked(context.Background(), "jti")
			if err != nil {
				t.Fatalf("IsRevoked: %v", err)
			}
			if got != tt.want {
				t.Fatalf("IsRevoked = %v, want %v", got, tt.want)
			}
		})
	}
}
