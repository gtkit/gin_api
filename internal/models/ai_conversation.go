package models

import "time"

// AIConversation 列宽常量。写入点按它们截断：严格 sql_mode 下 varchar 溢出是
// 整条写入失败，而标题取自用户第一句话——一句长文案就能让整轮对话存不进去，
// 且失败发生在模型已经回答完之后。
const (
	AIConversationTitleLen    = 120
	AIConversationProviderLen = 32
	AIConversationModelLen    = 64
)

// AIConversation 是一次多轮对话的会话头。
//
// 它与 AIMessage 构成一个聚合：会话是聚合根，消息是其内部实体，两者的读写
// 收口在 repository/aiconversation。计数与 token 累计冗余在这里而不是每次
// 对 ai_messages 做聚合查询——会话列表是最高频的读，而按 conversation_id
// 做 COUNT/SUM 的成本随对话长度线性增长。
//
// 冗余的代价是它必须与消息表在**同一事务**里更新，否则两者会分叉。
type AIConversation struct {
	BaseID

	// UserID 是会话归属人。所有读写都必须带上它做归属校验——
	// 只按 conversation_id 查会让任何人凭猜 ID 读到别人的对话。
	UserID int64 `gorm:"column:user_id;not null;index:idx_ai_conversations_user_active,priority:1;comment:归属用户ID"`
	// Title 是会话标题，默认取首轮提问的前若干字符，可由用户改写。
	Title string `gorm:"column:title;size:120;not null;default:'';comment:会话标题"`
	// Provider / Model 记录**最近一次**成功回复所用的厂商与模型。
	//
	// 不做"整个会话锁定一个模型"：同一个会话中途换模型是常见用法（先用便宜的
	// 试，不满意再换强的）。逐条消息的真实取值另存在 AIMessage 上，这里只是
	// 会话列表要展示的摘要。
	Provider string `gorm:"column:provider;size:32;not null;default:'';comment:最近使用的厂商"`
	Model    string `gorm:"column:model;size:64;not null;default:'';comment:最近使用的模型"`

	// MessageCount 是已落库的消息条数（含 user 与 assistant）。
	MessageCount int `gorm:"column:message_count;not null;default:0;comment:消息条数"`
	// PromptTokens / CompletionTokens 是本会话累计用量，用于成本归因。
	//
	// 用 int64 而不是 int：32 位平台上 int 是 32 位，而一个长期会话的累计
	// 输入 token 完全可能越过 21 亿——溢出后变成负数，且不会有任何报错。
	PromptTokens     int64 `gorm:"column:prompt_tokens;not null;default:0;comment:累计输入token"`
	CompletionTokens int64 `gorm:"column:completion_tokens;not null;default:0;comment:累计输出token"`

	// LastMessageAt 是最后一条消息的时间，会话列表按它倒序。
	//
	// 不复用 UpdatedAt：改标题也会动 UpdatedAt，而那不该让会话在列表里
	// 跳到最前面——列表表达的是"最近聊过什么"，不是"最近改过什么"。
	LastMessageAt *time.Time `gorm:"column:last_message_at;type:datetime;index:idx_ai_conversations_user_active,priority:2,sort:desc;comment:最后消息时间"`

	BaseTimeField
}

// TableName 指定表名。
func (c *AIConversation) TableName() string {
	return "ai_conversations"
}
