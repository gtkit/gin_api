package aiconversation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gtkit/ormx/paginator"
	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/pkg/paging"
)

// listSortMapping 是会话列表的排序白名单：外部排序键 → 受信任列名。
//
// 动态排序列进不了参数占位符，白名单是 ORM 项目里防注入的唯一手段
// （docs/db-patterns.md「防御性查询」）。未命中的键由执行器回退默认排序（主键）。
var listSortMapping = map[string]string{
	"id":              "id",
	"created_at":      "created_at",
	"last_message_at": "last_message_at",
}

// GetOwned 按主键取会话，**并强制归属校验**。
//
// 归属条件写在 SQL 里而不是取回后在调用方比对：比对是可以忘的，而忘掉的后果是
// 任何人凭猜 ID 就能读到别人的对话。把它钉进查询，忘不掉。
//
// found=false 表示不存在或不属于该用户——两者**刻意不区分**：区分开等于告诉
// 调用方"这个 ID 存在，只是不是你的"，那是一条可枚举的存在性信道。
func (r *Repository) GetOwned(
	ctx context.Context, userID, id int64,
) (*models.AIConversation, bool, error) {
	if userID <= 0 || id <= 0 {
		return nil, false, nil
	}

	var conv models.AIConversation
	err := r.mdbCtx(ctx).
		Where("id = ?", id).
		Where("user_id = ?", userID).
		First(&conv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get ai conversation %d: %w", id, err)
	}
	return &conv, true, nil
}

// List 分页列出某用户的会话。
//
// 默认排序显式指定为 last_message_at 倒序：不指定的话执行器按主键排，
// 而主键顺序等于"创建顺序"——一个很久以前建、昨天刚聊过的会话会排在末尾，
// 与"最近聊过什么"这个列表本意相反。
func (r *Repository) List(
	ctx context.Context, userID int64, query paging.Query,
) ([]*models.AIConversation, paging.Paging, error) {
	query.PageSize = clampPageSize(query.PageSize)
	if query.Sort == "" {
		query.Sort = "last_message_at"
		query.Order = "desc"
	}

	q := r.mdbCtx(ctx).
		Model(&models.AIConversation{}).
		Where("user_id = ?", userID)

	page, err := paginator.Paginate[*models.AIConversation](q, paginator.Params{
		Page:     query.Page,
		PageSize: query.PageSize,
		Sort:     query.Sort,
		Order:    query.Order,
	}, paginator.WithSortMapping(listSortMapping))
	if err != nil {
		return nil, paging.Paging{}, fmt.Errorf("list ai conversations: %w", err)
	}

	return page.Items, paging.Paging{
		CurrentPage: page.CurrentPage,
		PageSize:    page.PageSize,
		TotalPage:   page.TotalPage,
		TotalCount:  page.TotalCount,
	}, nil
}

// Rename 改写会话标题；updated=false 表示会话不存在或不属于该用户。
//
// 用一条带归属条件的 UPDATE 而不是"先查再改"：后者在两条语句之间存在窗口，
// 且多付一次往返。返回受影响行数即可回答"改到了没有"。
func (r *Repository) Rename(ctx context.Context, userID, id int64, title string) (bool, error) {
	if userID <= 0 || id <= 0 {
		return false, nil
	}

	res := r.mdbCtx(ctx).
		Model(&models.AIConversation{}).
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Update("title", title)
	if res.Error != nil {
		return false, fmt.Errorf("rename ai conversation %d: %w", id, res.Error)
	}
	// RowsAffected 为 0 有两种可能：没匹配到，或标题与原值相同。后者按"改到了"
	// 处理才对——调用方问的是"这个会话归你且已是这个标题吗"，两种情况的答案
	// 都是肯定的。因此这里补一次存在性判断，而不是直接返回 RowsAffected > 0。
	if res.RowsAffected > 0 {
		return true, nil
	}
	_, found, err := r.GetOwned(ctx, userID, id)
	return found, err
}

// Delete 软删除会话。deleted=false 表示会话不存在或不属于该用户。
//
// **只软删会话头，不动消息**：消息表按 conversation_id 关联，而会话一旦软删，
// 任何经 GetOwned 的读取都取不到它，消息也就再无入口。给几百上千条消息逐条
// 打软删标记，换来的只是同一个不可见性，代价却是一次可能很长的事务。
//
// 真正的物理清理（含消息）应当由保留期任务按 deleted_at 批量执行，
// 那是另一条需要按司法辖区决定保留期的路径，不在本方法职责内。
func (r *Repository) Delete(ctx context.Context, userID, id int64) (bool, error) {
	if userID <= 0 || id <= 0 {
		return false, nil
	}

	res := r.mdbCtx(ctx).
		Where("id = ?", id).
		Where("user_id = ?", userID).
		Delete(&models.AIConversation{})
	if res.Error != nil {
		return false, fmt.Errorf("delete ai conversation %d: %w", id, res.Error)
	}
	return res.RowsAffected > 0, nil
}

// touch 在同一事务里把会话的汇总列推进一轮。
//
// 用 gorm.Expr 做**原地自增**而不是"读出来加一再写回"：后者在并发的两轮对话
// 之间会丢更新（两个事务读到同一个旧值），而这里的计数正是要用来对账用量的。
func touch(tx *gorm.DB, conv *models.AIConversation, assistant *models.AIMessage, at time.Time) error {
	updates := map[string]any{
		"message_count":     gorm.Expr("message_count + ?", 2),
		"prompt_tokens":     gorm.Expr("prompt_tokens + ?", assistant.PromptTokens),
		"completion_tokens": gorm.Expr("completion_tokens + ?", assistant.CompletionTokens),
		"last_message_at":   at,
		"provider":          assistant.Provider,
		"model":             assistant.Model,
	}
	if err := tx.Model(&models.AIConversation{}).
		Where("id = ?", conv.ID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("touch ai conversation %d: %w", conv.ID, err)
	}
	return nil
}
