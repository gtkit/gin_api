//go:build integration

package testutil

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 真实 Redis 集成测试的连接来源与默认值。
//
// 刻意不走 config.Load()：这批测试需要一个专用 db 序号，
// 而应用配置的 redis.dbs 只声明业务用到的那几个；借业务 db 跑测试会写入业务数据。
// internal/pkg/ratelimit 保留的连接辅助函数采用同一套环境变量与专用库约定。
const (
	redisAddrEnv     = "REDIS_ITEST_ADDR"
	redisPasswordEnv = "REDIS_ITEST_PASSWORD"
	redisDBEnv       = "REDIS_ITEST_DB"
	scriptFlushEnv   = "ALLOW_REDIS_SCRIPT_FLUSH"

	defaultRedisAddr = "127.0.0.1:6379"
	// 默认 db 15：Redis 默认 16 个库（0-15），业务通常从 0 往上用，
	// 最后一个最不容易撞上。撞了就用 REDIS_ITEST_DB 换。
	defaultRedisDB = 15
)

// SkipOrFailIntegration 统一集成测试的逃生口语义，与 repository 那批一致：
// 默认（本地无依赖）Skip；REQUIRE_DB_INTEGRATION=1 时 Skip 变 Fatal——
// 强制 job 里环境异常必须是红灯，不允许绿灯里藏 SKIP。
func SkipOrFailIntegration(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("REQUIRE_DB_INTEGRATION") == "1" {
		t.Fatalf("REQUIRE_DB_INTEGRATION=1 但集成前置不满足: "+format, args...)
	}
	t.Skipf(format, args...)
}

// RealRedisOrSkip 返回连到**真实 Redis** 的客户端；连不上则按上面的语义 Skip/Fail。
//
// 用专用 db 而不是业务 db：这批测试会写键、删键、并在显式授权下执行 SCRIPT FLUSH，
// 全都不该落在有真实数据的库上。
func RealRedisOrSkip(t *testing.T) *redis.Client {
	t.Helper()

	addr := envOr(redisAddrEnv, defaultRedisAddr)
	db := defaultRedisDB
	if raw := os.Getenv(redisDBEnv); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("%s=%q is not a number", redisDBEnv, raw)
		}
		db = parsed
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv(redisPasswordEnv),
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("close unreachable Redis client: %v", closeErr)
		}
		SkipOrFailIntegration(t, "real redis unreachable at %s db %d: %v", addr, db, err)
		return nil
	}

	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close Redis client: %v", err)
		}
	})
	return client
}

// UniqueKeyPrefix 返回本次测试独占的键前缀，并登记只删该前缀下的键的清理动作。
//
// 只删自己的键、不用 FLUSHDB：即便测试跑在专用库上，也不该假设那个库只有自己在用——
// 一次误配的 REDIS_ITEST_DB 会让 FLUSHDB 清掉别人的数据，而那是不可逆的。
func UniqueKeyPrefix(t *testing.T, client *redis.Client) string {
	t.Helper()

	prefix := "itest:" + strconv.FormatInt(time.Now().UnixNano(), 36) + ":"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, prefix+"*", 256).Result()
			if err != nil {
				t.Errorf("cleanup scan for %s*: %v", prefix, err)
				return
			}
			if len(keys) > 0 {
				if err := client.Del(ctx, keys...).Err(); err != nil {
					t.Errorf("cleanup del for %s*: %v", prefix, err)
					return
				}
			}
			if next == 0 {
				return
			}
			cursor = next
		}
	})
	return prefix
}

// RequireScriptFlushPermission 把 SCRIPT FLUSH 类用例挡在显式授权之后。
//
// **用普通 Skip 而不是 SkipOrFailIntegration**，这个区分是刻意的：
//   - SkipOrFailIntegration 表达「环境不满足」，强制 job 里应当是红灯；
//   - 这里表达「刻意的 opt-in」，因为 SCRIPT FLUSH 是**服务器级**操作，
//     会清掉同一实例上其它应用的脚本缓存。共享的开发 Redis 上不该默认执行。
//
// 未授权时跳过，不是"环境坏了"，所以即便 REQUIRE_DB_INTEGRATION=1 也不该失败。
func RequireScriptFlushPermission(t *testing.T) {
	t.Helper()
	if os.Getenv(scriptFlushEnv) != "1" {
		t.Skipf("set %s=1 to allow SCRIPT FLUSH; it is server-wide and evicts other "+
			"applications' cached scripts on the same instance", scriptFlushEnv)
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
