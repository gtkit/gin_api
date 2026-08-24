package ratelimit

import (
	"context"
	"errors"
	"testing"
)

// TestProbeCleanupFailureDoesNotFailConstruction 钉住"清理失败不是能力失败"。
//
// 缺陷形态：探针在删除临时键失败时 `return err`，而构造函数把 probe 的任何
// 非 nil 返回都当能力失败。于是 Redis ACL 允许 EVALSHA 但不允许 DEL——最小
// 权限配置的常见形态——会让含 fail_closed 分布式策略的进程**永久起不来**，
// 而限流能力本身完好、能力校验已经通过。
//
// 这个方向的失败不是保守，是错误：它以"更安全"的名义拒绝了一个可用的系统。
//
// 清理确实可以丢弃：两个探针键（桶键与注册表键）都在创建它们的同一段 Lua 里
// PEXPIRE（script.go 的 `PEXPIRE registry` 与 `PEXPIRE KEYS[1]`），因此清理
// 失败的唯一后果是它们多存活一个 TTL 周期后自行消失。
func TestProbeCleanupFailureDoesNotFailConstruction(t *testing.T) {
	t.Parallel()

	fake := &fakeRedis{
		// 能力校验正常：脚本可加载、EvalSha 返回放行判定。
		delErr: errors.New("NOPERM this user has no permissions to run the 'del' command"),
	}

	backend, err := NewRedisBackend(context.Background(), fake, WithKeyPrefix("svc"))
	if err != nil {
		t.Fatalf("仅 DEL 无权限时构造必须成功，得到错误: %v\n"+
			"限流从不删除键（键靠 TTL 回收），DEL 权限缺失不影响任何运行期路径", err)
	}
	if backend == nil {
		t.Fatal("构造成功却返回了 nil 后端")
	}

	// 清理确实被尝试过——否则本测试证明的只是"没调用 Del"。
	if _, _, del := fake.counts(); del == 0 {
		t.Error("探针未尝试清理临时键：清理是尽力而为，但必须真的尝试")
	}
}

// TestProbeCapabilityFailureStillFailsConstruction 是上一条的反向锚。
//
// 缺了它，一个"探针所有错误都不致命"的实现也能让上面的用例通过——而那会让
// 一个 ACL 不允许 EVALSHA 的实例被判为可用，问题推迟到第一个真实请求。
func TestProbeCapabilityFailureStillFailsConstruction(t *testing.T) {
	t.Parallel()

	t.Run("脚本无法加载", func(t *testing.T) {
		t.Parallel()
		fake := &fakeRedis{loadErr: errors.New("NOPERM no permissions to run 'script'")}
		if _, err := NewRedisBackend(context.Background(), fake); err == nil {
			t.Fatal("脚本无法加载时构造必须失败")
		}
	})

	t.Run("探针调用报错", func(t *testing.T) {
		t.Parallel()
		fake := &fakeRedis{evalErrs: []error{errors.New("NOPERM no permissions to run 'evalsha'")}}
		if _, err := NewRedisBackend(context.Background(), fake); err == nil {
			t.Fatal("能力校验失败时构造必须失败")
		}
	})
}
