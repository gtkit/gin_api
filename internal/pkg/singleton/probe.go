package singleton

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// probeTTL 是探测租约的存活时间。
//
// 取得很短：探测键即使因为进程崩溃没能清理掉，也会很快自己消失，
// 不会在 Redis 里留下垃圾，更不会挡住后续的探测。
const probeTTL = 5 * time.Second

// Probe 用一个随机命名的探测租约真跑一遍全部操作。
//
// **不能只用 PING 就宣称锁能力可用**。Redis 活着不代表：
//
//   - 当前 ACL 允许执行 EVAL（不少生产 Redis 会限制脚本）；
//   - 锁键与 fence 键落在同一个集群 slot（跨 slot 的脚本会被直接拒绝）；
//   - 脚本的返回值形状与本包的解析一致（Redis 版本差异）。
//
// 这三条里任何一条不成立，锁在第一次真正需要它的时候才会失败——而那时
// 通常已经有两个实例在跑同一个任务了。因此这里在启动期就把它们全试一遍。
//
// 探测用**独立的名字**，不碰真实任务的锁：用真名探测会把正在运行的实例
// 的租约搅乱，那比不探测更糟。
func Probe(ctx context.Context, client LeaseClient, prefix string) error {
	name, err := randomToken()
	if err != nil {
		return err
	}

	// 探测租约的名字带随机段：多个实例同时启动时不会互相干扰，
	// 而"探测失败"必须只反映 Redis 的能力，不反映其它实例的时序。
	// 构造零 I/O；脚本能力（含 ACL 是否允许 EVAL/SCRIPT LOAD）由下面的
	// 真实操作经 NOSCRIPT 自愈路径实测。
	lease, err := newProbeLease(client, prefix, "probe-"+name)
	if err != nil {
		return fmt.Errorf("singleton: probe lease construction failed: %w", err)
	}

	// 1. 抢锁 + fence 生成。
	held, err := lease.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("singleton: probe could not acquire a lease (if this is a Redis "+
			"Cluster, check that the lock and fence keys share a hash tag): %w", err)
	}
	if held.Fence <= 0 {
		return fmt.Errorf("singleton: probe acquired a lease but got fence %d: the script's "+
			"return shape does not match what this package parses", held.Fence)
	}

	// 尽力清理：即使后面的步骤失败，也不留一个占着五秒的锁。
	defer func() { _ = lease.Release(context.WithoutCancel(ctx), held) }()

	// 2. compare-and-renew 对**自己的**锁必须成功。
	if err := lease.Renew(ctx, held); err != nil {
		return fmt.Errorf("singleton: probe could not renew its own lease: %w", err)
	}

	// 3. compare-and-renew 对**别人的** token 必须失败。
	//
	// 这一条是探测里最有价值的：它验证续租脚本真的做了比对。一个写成裸
	// PEXPIRE 的实现会在这里"成功"，而那正是最危险的错误——它让旧实例
	// 延长新实例的锁，两个实例同时在跑且都以为自己独占。
	foreign := Held{Token: held.Token + "-not-mine", Fence: held.Fence}
	if err := lease.Renew(ctx, foreign); !errors.Is(err, ErrLeaseLost) {
		return fmt.Errorf("singleton: probe renewed a lease it does not own (got %v): "+
			"the renew script is not comparing the token, so a stale instance would keep "+
			"extending the current holder's lease while believing it still owns one", err)
	}

	// 4. 抢锁必须排他。
	if _, err := lease.Acquire(ctx); !errors.Is(err, ErrNotAcquired) {
		return fmt.Errorf("singleton: probe acquired an already-held lease (got %v): "+
			"the lock is not exclusive", err)
	}

	// 5. compare-and-delete 对别人的 token 必须不删。
	if err := lease.Release(ctx, foreign); err != nil {
		return fmt.Errorf("singleton: probe release with a foreign token errored: %w", err)
	}
	if _, err := lease.Acquire(ctx); !errors.Is(err, ErrNotAcquired) {
		return errors.New("singleton: probe deleted a lock it does not own: one expired " +
			"lease would escalate into several instances running at once")
	}

	// 6. 自己的释放必须真的释放。
	if err := lease.Release(ctx, held); err != nil {
		return fmt.Errorf("singleton: probe could not release its own lease: %w", err)
	}
	if _, err := lease.Acquire(ctx); err != nil {
		return fmt.Errorf("singleton: probe could not re-acquire after releasing: %w", err)
	}

	return nil
}

// newProbeLease 构造一个**连 fence 计数器都会过期**的探测租约。
//
// 生产租约的 fence 计数器刻意不过期（清掉会让序号重新从 1 开始，而某个仍在
// 运行的旧实例可能持有更大的序号，fencing 会反过来起反作用）。探测租约没有
// 这个顾虑：它的名字是随机的、用完即弃、永远不会有下游写入引用它的序号。
// 若也不过期，每次进程启动都会在 Redis 里留下一个永不消失的计数器键。
func newProbeLease(client LeaseClient, prefix, name string) (*Lease, error) {
	lease, err := NewLease(client, prefix, name, probeTTL)
	if err != nil {
		return nil, err
	}
	lease.fenceTTL = probeTTL
	return lease, nil
}
