package config

import (
	"strings"
	"testing"
)

// TestValidateRedisDBZeroConsumers 覆盖"哪些能力要求 redis.dbs 含库 0"这条交叉校验。
//
// 为什么这条校验必须存在：资源层只打开 redis.dbs 声明过的库，取未声明的库得到
// nil 客户端。而 nil 客户端与"Redis 启动期不可达"在装配处无法区分，于是缺库 0
// 的后果是**指向错误方向的诊断**——fail_open 的限流策略静默进入进程级永久降级、
// 注销清理 worker 不注册、日志声称 Redis 不可达，而 Redis 完全健康。
// 运维会去查一个没有问题的 Redis。
//
// cache.redis_db 与 tasks.redis_db 早就有同款校验，这三处此前漏了。
func TestValidateRedisDBZeroConsumers(t *testing.T) {
	t.Parallel()

	// dbsWithoutZero 是触发条件：库 0 未被声明。
	dbsWithoutZero := []int{1, 2}

	tests := []struct {
		name string
		// mutate 启用某项要求库 0 的能力。
		mutate func(*Config)
		// wantHints 是错误里必须出现的线索：能力名 + 修法。
		wantHints []string
	}{
		{
			name: "限流使用 redis store",
			mutate: func(c *Config) {
				c.RateLimit.Policies = []RateLimitPolicyConfig{
					{ID: "p1", Rate: 1, Burst: 1, Scope: scopeIP, Store: storeRedis},
				}
			},
			wantHints: []string{"redis.dbs", "rate limit"},
		},
		{
			name: "本地认证启用",
			mutate: func(c *Config) {
				c.Auth.Mode = AuthModeLocal
			},
			wantHints: []string{"redis.dbs", "local authentication"},
		},
		{
			name: "注销保留期为正",
			mutate: func(c *Config) {
				c.Modules.User.DeactivationRetentionDays = 30
			},
			wantHints: []string{"redis.dbs", "deactivation"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := Config{Redis: RedisConfig{Addr: "127.0.0.1:6379", DBs: dbsWithoutZero}}
			tt.mutate(&cfg)

			err := validateRedisDBZeroConsumers(cfg)
			if err == nil {
				t.Fatal("启用了要求库 0 的能力而 dbs 不含 0，必须失败")
			}
			for _, hint := range tt.wantHints {
				if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(hint)) {
					t.Errorf("错误信息缺少线索 %q，运维无从判断改哪里: %v", hint, err)
				}
			}

			// 同一份配置补上库 0 后必须通过——否则这个用例证明的可能是别的错误。
			cfg.Redis.DBs = append([]int{0}, dbsWithoutZero...)
			if err := validateRedisDBZeroConsumers(cfg); err != nil {
				t.Fatalf("补上库 0 后仍失败: %v", err)
			}
		})
	}
}

// TestValidateRedisDBZeroReportsAllConsumersAtOnce 锁住"一条错误列出全部"。
//
// 逐项重复报错会让运维看到三行几乎相同的消息，而它们说的是同一件事。
func TestValidateRedisDBZeroReportsAllConsumersAtOnce(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Redis: RedisConfig{Addr: "127.0.0.1:6379", DBs: []int{1}},
		RateLimit: RateLimitConfig{Policies: []RateLimitPolicyConfig{
			{ID: "p1", Rate: 1, Burst: 1, Scope: scopeIP, Store: storeRedis},
		}},
		Auth: AuthConfig{Mode: AuthModeLocal},
	}
	cfg.Modules.User.DeactivationRetentionDays = 30

	err := validateRedisDBZeroConsumers(cfg)
	if err == nil {
		t.Fatal("三项能力同时启用而 dbs 缺 0，必须失败")
	}
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{"rate limit", "local authentication", "deactivation"} {
		if !strings.Contains(msg, hint) {
			t.Errorf("错误信息未列出 %q: %v", hint, err)
		}
	}
	// 一条错误，不是三条：换行数用来近似"报了几条"。
	if n := strings.Count(err.Error(), "\n"); n > 0 {
		t.Errorf("应聚合为一条错误，实际含 %d 个换行: %v", n, err)
	}
}

// TestValidateRedisDBZeroSkipsWhenNoConsumer 是反向断言，防止把校验写成无条件要求。
//
// 缺了它，一个"总是要求 dbs 含 0"的实现也能让上面的用例全绿——
// 而那会逼着不用这些能力的部署声明一个用不到的库。
func TestValidateRedisDBZeroSkipsWhenNoConsumer(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Redis: RedisConfig{Addr: "127.0.0.1:6379", DBs: []int{1, 2}},
		// 限流策略走 local store；认证走外部模式；注销保留期为 0。
		RateLimit: RateLimitConfig{Policies: []RateLimitPolicyConfig{
			{ID: "p1", Rate: 1, Burst: 1, Scope: scopeIP, Store: storeLocal},
		}},
		Auth: AuthConfig{Mode: AuthModeExternal},
	}

	if err := validateRedisDBZeroConsumers(cfg); err != nil {
		t.Fatalf("没有任何能力需要库 0，不该要求它: %v", err)
	}
}
