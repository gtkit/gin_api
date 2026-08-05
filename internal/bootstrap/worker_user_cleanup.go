package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/pkg/singleton"

	userworker "github.com/gtkit/gin-api/internal/module/user/worker"
)

// cleanupTaskID 是清理任务的稳定标识，同时用作租约名。
//
// 租约名进 Redis 键，因此必须稳定：改名等于换一把锁，升级期间新旧实例
// 会各持一把，单实例保证在那段时间里完全失效。
//
// 名字里避开 "Name"/"Key"/"Secret" 这类词：gosec 的硬编码凭据规则按标识符
// 命名加字符串字面量匹配，`cleanupTaskID` 会被判成 G101。加 //nolint
// 是另一条路，但那要求每个读代码的人都去确认豁免是否合理——换个名字
// 让规则根本不触发更省事，也不会掩盖将来真正的硬编码凭据。
const cleanupTaskID = "user.deactivation-cleanup"

// cleanupLeaseTTL 是清理任务的租期。
//
// 取两分钟：崩溃后另一个实例最多等两分钟接手，而续租每 40 秒一次
// （租期的 1/3），留出两次重试的余量。这个任务不紧急，租期长一点换来的是
// 对网络抖动更宽的容忍。
const cleanupLeaseTTL = 2 * time.Minute

// newDeactivationCleanupWorker 把清理任务包成**单实例** worker。
//
// 多副本部署下每个实例都会跑这个任务。清理本身是幂等的（批量
// `DELETE ... WHERE created_at < ? LIMIT n`，重复执行影响 0 行），
// 因此重复执行只是浪费——但浪费的是数据库连接与 binlog，而这个任务
// 本来就不紧急，没有理由让 N 个副本同时做同一件事。
//
// cleanup 为 nil（未配置保留期）时返回 nil，调用方据此不注册。
// client 为 nil 时返回错误：**没有 Redis 就没有单实例保证**，而静默退回到
// "每个副本各跑各的"会让人以为有保护——那比明确报错更危险。
func newDeactivationCleanupWorker(
	ctx context.Context,
	cleanup *userworker.DeactivationCleanup,
	client singleton.LeaseClient,
	prefix string,
	recorder *metrics.Recorder,
) (Worker, error) {
	if cleanup == nil {
		return nil, nil
	}
	if client == nil {
		return nil, fmt.Errorf(
			"worker %q needs redis for its single-instance lease: without it every replica "+
				"would run the cleanup concurrently. Either configure redis, or set "+
				"modules.user.deactivation_retention_days to 0 and handle retention "+
				"outside this service", cleanupTaskID)
	}

	lease, err := singleton.NewLease(ctx, client, prefix, cleanupTaskID, cleanupLeaseTTL)
	if err != nil {
		return nil, fmt.Errorf("build lease for worker %q: %w", cleanupTaskID, err)
	}

	runner, err := singleton.NewRunner(lease,
		func(taskCtx context.Context, _ singleton.Held) error {
			// 不传 fence：本任务按**严格幂等**注册（见下方 Guarantee）。
			// 声明 fenced 却不把序号传给写路径，等于什么保护也没有，
			// 而它看起来是有的。
			return cleanup.RunOnce(taskCtx)
		},
		singleton.Config{
			Interval: cleanup.Interval(),
			// **严格幂等**：批量 DELETE 重复执行影响 0 行，结果与跑一次相同。
			// 因此不需要 fence 序号——两个实例同时跑只是浪费，不产生错误结果。
			Guarantee: singleton.GuaranteeIdempotent,
			// 单轮上限取间隔的一半：超过它说明积压已经大到追不上，
			// 中止并等下一轮，而不是让一轮无限期跑下去把租约一直占着。
			MaxRuntime: cleanup.Interval() / 2,
			Observer:   singletonObserver{recorder: recorder},
		})
	if err != nil {
		return nil, fmt.Errorf("build runner for worker %q: %w", cleanupTaskID, err)
	}

	return newSingletonWorker(cleanupTaskID, runner, client, prefix), nil
}
