package application

import (
	"context"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/pkg/paging"
)

// ConversationStore 定义 ai 模块依赖的持久化能力。
//
// 接口按**使用方**定义（ports & adapters）：本层声明需要什么，
// internal/repository/aiconversation 隐式满足它。这样换一个存储只需要换一个
// 实现，本层与 transport 层都不用动。
//
// 方法签名直接用 models.*：它们是只读持久化结构与枚举，
// docs/architecture.md 允许 application 引用（"允许但应克制"）。为这几个方法
// 再定义一层 application 私有结构，产生的只是逐字段拷贝，而每加一个列就要
// 在两处各改一遍——那正是漏改的来源。
type ConversationStore interface {
	// SaveTurn 原子写入一轮对话：会话（ID 为 0 时新建）+ 用户提问 + 模型回复
	// + 会话汇总列推进。成功后 conv.ID 已回填。
	SaveTurn(ctx context.Context, conv *models.AIConversation, user, assistant *models.AIMessage) error

	// GetOwned 取会话并强制归属校验；found=false 表示不存在**或**不属于该用户，
	// 两者刻意不区分（区分开等于提供一条可枚举的存在性信道）。
	GetOwned(ctx context.Context, userID, id int64) (*models.AIConversation, bool, error)

	// List 分页列出某用户的会话。
	List(ctx context.Context, userID int64, query paging.Query) ([]*models.AIConversation, paging.Paging, error)

	// RecentMessages 取会话最近 limit 条消息，按对话顺序（id 升序）返回。
	RecentMessages(ctx context.Context, conversationID int64, limit int) ([]*models.AIMessage, error)

	// ListMessages 分页列出会话内的消息。**不做归属校验**，调用方必须先经 GetOwned。
	ListMessages(ctx context.Context, conversationID int64, query paging.Query) ([]*models.AIMessage, paging.Paging, error)

	// Rename 改写标题；updated=false 表示会话不存在或不属于该用户。
	Rename(ctx context.Context, userID, id int64, title string) (bool, error)

	// Delete 软删会话；deleted=false 表示会话不存在或不属于该用户。
	Delete(ctx context.Context, userID, id int64) (bool, error)
}

// Role 是消息在一次调用里的角色（协议中立）。
type Role string

// 角色取值，与各厂商协议里的 role 字段一致。
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ChatMessage 是发给模型的一条消息。
type ChatMessage struct {
	Role    Role
	Content string
}

// ChatCall 是一次模型调用的中立描述。
//
// 它不是 SDK 的请求结构：本层不认识任何 LLM SDK，装配层的 adapter 负责翻译。
// 这条边界的价值在换库时才显现——换掉 SDK 只改 adapter，业务编排一行不动。
type ChatCall struct {
	// Provider 为空表示用配置里的默认厂商。
	Provider string
	// Model 为空表示用该厂商的默认模型。
	Model       string
	Messages    []ChatMessage
	MaxTokens   int
	Temperature *float32
}

// Usage 是一次调用的 token 用量。
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Reply 是一次模型调用的结果。
type Reply struct {
	Content   string
	Reasoning string
	// Provider / Model 是**实际生效**的厂商与模型，不是请求里写的那个：
	// 请求可以留空走默认，而落库要记的是真实取值，否则成本归因对不上账。
	Provider     string
	Model        string
	FinishReason string
	Usage        Usage
}

// ProviderInfo 描述一个已启用的厂商。
type ProviderInfo struct {
	Name string
	// Model 是该厂商生效的默认模型。
	Model string
	// Default 标记它是否为配置里的默认厂商。
	Default bool
}

// LLMClient 定义调用大模型的能力。
type LLMClient interface {
	// Providers 返回已启用的厂商列表（有序）。
	Providers() []ProviderInfo

	// Chat 发起一次非流式调用。
	Chat(ctx context.Context, call ChatCall) (Reply, error)

	// Stream 发起一次流式调用，每个增量文本片段调用一次 onDelta。
	//
	// **返回的 Reply 在 err != nil 时可能仍然携带已生成的部分内容**：流式调用
	// 中途断开（客户端走了、上游断了、预算到点）时，用户已经看到的那部分必须
	// 有机会落库，否则历史里会凭空缺一段。调用方据此决定是否保存部分结果。
	//
	// onDelta 返回错误即停止读取并把该错误上抛：写回客户端失败之后继续读上游
	// 没有意义，只是白白付掉剩余 token 的钱。
	Stream(ctx context.Context, call ChatCall, onDelta func(delta string) error) (Reply, error)
}

// Auditor 记录本模块的可观测事件。
//
// application 不依赖具体日志库，由 runtime 注入实现；未注入时静默跳过。
type Auditor interface {
	Auditf(format string, args ...any)
}
