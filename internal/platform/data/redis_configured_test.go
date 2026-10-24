package data

import "testing"

// TestRedisConfiguredDistinguishesTwoFailures 钉住"未配置"与"已配置但不可达"
// 可区分（resource-lifecycle spec）。
//
// 两者合并成"客户端为空"会让所有降级路径在它们最该生效的场景里不可达：
// 消费方看到空值只能按配置矛盾失败，于是一次 Redis 抖动把可降级的能力
// 变成整个进程起不来。redisx 在构造期就回收了不可达的客户端（见 openRedis），
// 因此这条区分只能由状态承载，不能靠指针非空性。
func TestRedisConfiguredDistinguishesTwoFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		res  *Resources
		want bool
	}{
		{name: "nil Resources 视为未配置", res: nil, want: false},
		{name: "未配置", res: &Resources{}, want: false},
		// 已配置但不可达：客户端为 nil（库回收了），状态仍表明配置存在。
		{name: "已配置但不可达", res: &Resources{redisConfigured: true}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.res.RedisConfigured(); got != tt.want {
				t.Fatalf("RedisConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}
