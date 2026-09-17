//go:build integration

// 真实 MySQL 集成测试：AI 对话聚合仓储与真实 schema 的一致性。
//
// 覆盖的轴——**只有真实数据库能回答的那几个**：
//
//   - 列名与真实 ai_conversations / ai_messages 表对不对得上（`conversation_id`
//     vs `conversationid` 这类）。列名错了表现为 Unknown column，而在 mock 里
//     永远不会出现。
//   - SaveTurn 的事务边界是否真的原子：会话、两条消息、汇总列四次写入要么全成
//     要么全不成。汇总列偏离真值是**静默**的，直到有人拿它对账。
//   - gorm.Expr 的原地自增在真实 SQL 上确实是 `col = col + ?`，而不是被当成
//     字面量写进去。
//   - 软删后 GetOwned 取不到（gorm.DeletedAt 的 clause 在真实查询里生效）。
//
// 安全设计：全部写操作包在事务里并强制回滚，零持久化，不污染库数据。
//
// 运行方式（DB 必须与本仓迁移状态一致，建议单开一个库，见 docs/testing.md）：
//
//	APP_ROOT=$(git rev-parse --show-toplevel) APP_DATABASE__NAME=gin_api_itest \
//	  go test -tags integration -run TestIntegration ./internal/repository/aiconversation/
package aiconversation_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/gtkit/ormx"
	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/pkg/paging"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/repository/aiconversation"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
)

// skipOrFail 统一集成测试的逃生口语义：
// 默认（本地无 DB）Skip；CI 设 REQUIRE_DB_INTEGRATION=1 时 Skip 变 Fatal——
// 强制 job 里环境异常必须是红灯，不允许绿灯里藏 SKIP。
func skipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("REQUIRE_DB_INTEGRATION") == "1" {
		t.Fatalf("REQUIRE_DB_INTEGRATION=1 但集成前置不满足: "+format, args...)
	}
	t.Skipf(format, args...)
}

// openDB 按仓库配置打开真实数据库；不可用时按 skipOrFail 语义处置。
func openDB(t *testing.T) *gorm.DB {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		skipOrFail(t, "config load failed (set APP_ROOT to repo root): %v", err)
		return nil
	}

	defer func() {
		if r := recover(); r != nil {
			skipOrFail(t, "mysql open panicked (db unreachable?): %v", r)
		}
	}()
	client := ormx.MustOpen(context.Background(),
		ormx.WithHost(cfg.Database.Host),
		ormx.WithPort(cfg.Database.Port),
		ormx.WithDatabase(cfg.Database.Name),
		ormx.WithUser(cfg.Database.Username),
		ormx.WithPassword(cfg.Database.Password),
	)
	return client.DB()
}

// errRollback 强制回滚，绝不提交。
var errRollback = errors.New("intentional rollback: integration test never persists")

const probeUserID int64 = 900001

// TestIntegrationSaveTurnAgainstRealSchema 验证一轮对话的完整写入路径。
func TestIntegrationSaveTurnAgainstRealSchema(t *testing.T) {
	db := openDB(t)
	if db == nil {
		return
	}

	repo := aiconversation.New(db)
	mgr := dbtx.NewManager(db)

	err := mgr.Do(context.Background(), func(ctx context.Context) error {
		conv := &models.AIConversation{UserID: probeUserID, Title: "集成测试会话"}
		user := &models.AIMessage{
			UserID: probeUserID, Role: models.AIRoleUser, Content: "问题",
		}
		assistant := &models.AIMessage{
			UserID: probeUserID, Role: models.AIRoleAssistant, Content: "回答",
			Provider: "deepseek", Model: "deepseek-chat",
			PromptTokens: 11, CompletionTokens: 13, TotalTokens: 24, FinishReason: "stop",
		}

		if e := repo.SaveTurn(ctx, conv, user, assistant); e != nil {
			return e
		}
		if conv.ID == 0 || user.ID == 0 || assistant.ID == 0 {
			return errors.New("主键未回填：调用方拿不到 conversation_id，客户端无法续聊")
		}
		// user 必须在 assistant 之前：组装上下文按 id 排序，顺序反了会让模型
		// 看到"先回答后提问"。
		if user.ID >= assistant.ID {
			return errors.New("消息主键顺序与对话顺序不一致")
		}

		got, found, e := repo.GetOwned(ctx, probeUserID, conv.ID)
		if e != nil {
			return e
		}
		if !found {
			return errors.New("刚写入的会话查不到 —— 查询条件与真实 schema 不一致")
		}
		// 汇总列必须由 gorm.Expr 原地自增得到，而不是把表达式当字面量写进去。
		if got.MessageCount != 2 || got.PromptTokens != 11 || got.CompletionTokens != 13 {
			return errors.New("会话汇总列没有按一轮对话推进：拿它对账会得到错的数")
		}
		if got.Provider != "deepseek" || got.Model != "deepseek-chat" {
			return errors.New("会话未记录最近使用的厂商/模型")
		}
		if got.LastMessageAt == nil {
			return errors.New("last_message_at 为空：会话会沉到列表底部")
		}

		// 再来一轮：自增必须叠加，而不是覆盖。
		if e := repo.SaveTurn(ctx, got,
			&models.AIMessage{UserID: probeUserID, Role: models.AIRoleUser, Content: "追问"},
			&models.AIMessage{
				UserID: probeUserID, Role: models.AIRoleAssistant, Content: "再答",
				Provider: "qwen", Model: "qwen-max", PromptTokens: 1, CompletionTokens: 2,
			}); e != nil {
			return e
		}
		got, _, e = repo.GetOwned(ctx, probeUserID, conv.ID)
		if e != nil {
			return e
		}
		if got.MessageCount != 4 || got.PromptTokens != 12 || got.CompletionTokens != 15 {
			return errors.New("第二轮没有叠加到汇总列：并发下的丢更新正是这样表现的")
		}

		return errRollback
	})
	if err != nil && !errors.Is(err, errRollback) {
		t.Fatalf("save turn against real schema failed: %v", err)
	}
}

// TestIntegrationOwnershipAndSoftDelete 验证归属条件与软删在真实查询里生效。
//
// 归属条件写在 SQL 里是本仓储防越权的**唯一**手段：它失效意味着任何人
// 凭猜 ID 就能读到别人的对话。
func TestIntegrationOwnershipAndSoftDelete(t *testing.T) {
	db := openDB(t)
	if db == nil {
		return
	}

	repo := aiconversation.New(db)
	mgr := dbtx.NewManager(db)

	err := mgr.Do(context.Background(), func(ctx context.Context) error {
		conv := &models.AIConversation{UserID: probeUserID, Title: "归属校验"}
		if e := repo.SaveTurn(ctx, conv,
			&models.AIMessage{UserID: probeUserID, Role: models.AIRoleUser, Content: "问"},
			&models.AIMessage{UserID: probeUserID, Role: models.AIRoleAssistant, Content: "答"},
		); e != nil {
			return e
		}

		// 换一个用户：必须查不到，且不能报错（"不存在"与"不是你的"不区分）。
		_, found, e := repo.GetOwned(ctx, probeUserID+1, conv.ID)
		if e != nil {
			return e
		}
		if found {
			return errors.New("另一个用户读到了不属于它的会话")
		}

		// 改名：他人无权改，本人可改。
		updated, e := repo.Rename(ctx, probeUserID+1, conv.ID, "被别人改了")
		if e != nil {
			return e
		}
		if updated {
			return errors.New("另一个用户改掉了不属于它的会话标题")
		}
		updated, e = repo.Rename(ctx, probeUserID, conv.ID, "新标题")
		if e != nil {
			return e
		}
		if !updated {
			return errors.New("本人改名失败")
		}
		// 改成同一个值仍应报告"改到了"：调用方问的是归属与终态，不是行数。
		updated, e = repo.Rename(ctx, probeUserID, conv.ID, "新标题")
		if e != nil {
			return e
		}
		if !updated {
			return errors.New("重复改成同一个标题被报成了会话不存在")
		}

		// 消息与分页：真实列名与排序键。
		msgs, page, e := repo.ListMessages(ctx, conv.ID, paging.Query{Page: 1, PageSize: 10})
		if e != nil {
			return e
		}
		if len(msgs) != 2 || page.TotalCount != 2 {
			return errors.New("消息分页与真实 schema 不一致")
		}
		recent, e := repo.RecentMessages(ctx, conv.ID, 1)
		if e != nil {
			return e
		}
		if len(recent) != 1 || recent[0].Role != models.AIRoleAssistant {
			return errors.New("RecentMessages 没有取到最后一条")
		}

		list, lp, e := repo.List(ctx, probeUserID, paging.Query{Page: 1, PageSize: 10})
		if e != nil {
			return e
		}
		if len(list) == 0 || lp.TotalCount == 0 {
			return errors.New("会话列表按 last_message_at 排序时与真实 schema 不一致")
		}

		// 软删后必须取不到：gorm.DeletedAt 的 clause 要在真实查询里生效。
		deleted, e := repo.Delete(ctx, probeUserID, conv.ID)
		if e != nil {
			return e
		}
		if !deleted {
			return errors.New("软删未命中")
		}
		_, found, e = repo.GetOwned(ctx, probeUserID, conv.ID)
		if e != nil {
			return e
		}
		if found {
			return errors.New("软删后仍能读到会话 —— deleted_at 条件没有生效")
		}

		return errRollback
	})
	if err != nil && !errors.Is(err, errRollback) {
		t.Fatalf("ownership/soft-delete against real schema failed: %v", err)
	}
}
