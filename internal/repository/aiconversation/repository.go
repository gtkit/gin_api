// Package aiconversation 是 AI 对话的**聚合仓储**：会话头与消息两张表由同一个
// 仓储读写。聚合边界见本包 README.md。
//
// 为什么不拆成两个一表一 repo：会话上的 message_count / prompt_tokens /
// completion_tokens 是消息的冗余汇总，必须与消息写入原子提交。拆开之后，
// "同时更新两张表"这条不变量就只能靠调用方每次都记得开事务并按正确顺序调用
// 两个仓储——而漏掉一次不会有任何报错，只会让计数悄悄偏离真值。
package aiconversation

import (
	"context"

	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/repository/dbtx"
)

// Repository 是 ai_conversations + ai_messages 的数据访问。
type Repository struct {
	db *gorm.DB
}

// New 构造仓储。
func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// mdbCtx 统一经 dbtx.DB 收口，使本 repo 的数据访问自动参与 TxManager 开启的事务。
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB {
	return dbtx.DB(ctx, r.db)
}

// 分页钳制值。
//
// 防御钳制与 application 的归一化同值：本仓储也可能被 application 之外的调用方
// 直接使用，无界查询不能指望上游都归一化过——一个 page_size=100000 的请求会把
// 整张消息表连同 mediumtext 正文一起搬进内存。
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

func clampPageSize(size int) int {
	if size <= 0 {
		return defaultPageSize
	}
	if size > maxPageSize {
		return maxPageSize
	}
	return size
}
