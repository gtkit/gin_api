package aiconversation

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/gtkit/ormx/paginator"
	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/pkg/paging"
)

// messageSortMapping 是消息列表的排序白名单（理由同 listSortMapping）。
var messageSortMapping = map[string]string{
	"id":         "id",
	"created_at": "created_at",
}

// SaveTurn 原子写入一轮对话：会话（不存在则新建）+ 用户提问 + 模型回复 + 汇总列推进。
//
// 为什么会话与消息在**同一事务**里创建，而不是先建会话再调模型：
// 先建会话的话，一次上游失败会留下一个空会话——用户在列表里看到一条什么都没有
// 的记录，而它既不能续聊也说不清是什么。放在一起写，上游失败就什么都不留。
//
// 事务由本仓储自己开：这是**聚合内部**的多语句写，边界完全在本包内，不需要
// application 经 TxManager 编排（docs/architecture.md「跨 repo 事务」只约束
// 跨多个仓储的场景）。已在外层事务中时 GORM 退化为 savepoint，语义仍然成立。
//
// 成功返回后 conv.ID 与两条消息的 ID 已被回填。
//
// **调用方必须保证事务外不含任何网络调用**：模型调用在此之前已经结束，
// 这里只做本地写入——把外部调用包进事务是长事务与连接池耗尽的典型来源。
func (r *Repository) SaveTurn(
	ctx context.Context, conv *models.AIConversation, user, assistant *models.AIMessage,
) error {
	if conv == nil || user == nil || assistant == nil {
		return fmt.Errorf("save ai turn: conversation, user message and assistant message are all required")
	}

	// 时间由本方法统一取一次：两条消息与会话的 last_message_at 必须一致，
	// 各自取 time.Now() 会让"最后消息时间"早于最后那条消息的 created_at。
	at := time.Now()

	return r.mdbCtx(ctx).Transaction(func(tx *gorm.DB) error {
		if conv.ID == 0 {
			if conv.LastMessageAt == nil {
				conv.LastMessageAt = &at
			}
			if err := tx.Create(conv).Error; err != nil {
				return fmt.Errorf("create ai conversation: %w", err)
			}
		}

		user.ConversationID = conv.ID
		assistant.ConversationID = conv.ID

		// 两条消息一次 Create：一次往返而不是两次，且顺序由切片决定，
		// 自增主键因此保证 user 在前——组装上下文时按 id 排序才是对话顺序。
		batch := []*models.AIMessage{user, assistant}
		if err := tx.Create(&batch).Error; err != nil {
			return fmt.Errorf("create ai messages: %w", err)
		}

		return touch(tx, conv, assistant, at)
	})
}

// RecentMessages 取会话最近 limit 条消息，**按对话顺序（id 升序）返回**。
//
// 查询本身按 id 倒序取前 limit 条再反转，而不是升序取"最后 limit 条"：
// 后者需要先知道总数或做 OFFSET，两者的成本都随对话长度增长，而这是每一轮
// 对话都要走的路径。
//
// 不投影掉 content：正文正是要拿来组装上下文的东西。reasoning 则被排除——
// 推理过程不回传给模型（各厂商的协议里它不是可回填的输入），白读一列 mediumtext。
func (r *Repository) RecentMessages(
	ctx context.Context, conversationID int64, limit int,
) ([]*models.AIMessage, error) {
	if conversationID <= 0 || limit <= 0 {
		return nil, nil
	}

	var messages []*models.AIMessage
	err := r.mdbCtx(ctx).
		Model(&models.AIMessage{}).
		Select("id", "conversation_id", "user_id", "role", "content", "provider", "model", "created_at").
		Where("conversation_id = ?", conversationID).
		Order("id DESC").
		Limit(limit).
		Find(&messages).Error
	if err != nil {
		return nil, fmt.Errorf("load recent ai messages of conversation %d: %w", conversationID, err)
	}

	slices.Reverse(messages)
	return messages, nil
}

// ListMessages 分页列出会话内的消息，默认按 id 升序（对话顺序）。
//
// **不做归属校验**：调用方必须先经 GetOwned 确认会话归属。这里再查一次等于
// 每次列消息都多一次往返，而调用点只有一处——把它写进 application 的编排里，
// 由那一处的测试盯住。
func (r *Repository) ListMessages(
	ctx context.Context, conversationID int64, query paging.Query,
) ([]*models.AIMessage, paging.Paging, error) {
	query.PageSize = clampPageSize(query.PageSize)
	if query.Sort == "" {
		query.Sort = "id"
		query.Order = "asc"
	}

	q := r.mdbCtx(ctx).
		Model(&models.AIMessage{}).
		Where("conversation_id = ?", conversationID)

	page, err := paginator.Paginate[*models.AIMessage](q, paginator.Params{
		Page:     query.Page,
		PageSize: query.PageSize,
		Sort:     query.Sort,
		Order:    query.Order,
	}, paginator.WithSortMapping(messageSortMapping))
	if err != nil {
		return nil, paging.Paging{}, fmt.Errorf("list ai messages of conversation %d: %w", conversationID, err)
	}

	return page.Items, paging.Paging{
		CurrentPage: page.CurrentPage,
		PageSize:    page.PageSize,
		TotalPage:   page.TotalPage,
		TotalCount:  page.TotalCount,
	}, nil
}
