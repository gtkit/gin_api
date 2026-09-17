package models

// AIMessageRole 是一条消息在对话中的角色。
//
// 存 tinyint 而不是 varchar：角色是封闭集合，字符串列会让"assistant" 与
// "Assistant" 这类大小写差异悄悄进库，而按角色过滤的查询只会漏掉它们。
type AIMessageRole int8

const (
	// AIRoleSystem 是系统提示词。当前不落库（每轮由配置拼装），
	// 留出取值是因为"把某次特定的系统提示词固化进会话"是可预见的需求，
	// 而中途给枚举插值会让已有数据的含义改变。
	AIRoleSystem AIMessageRole = iota + 1
	// AIRoleUser 是用户提问。
	AIRoleUser
	// AIRoleAssistant 是模型回复。
	AIRoleAssistant
)

// Valid 报告角色取值是否在已定义集合内。
func (r AIMessageRole) Valid() bool {
	return r >= AIRoleSystem && r <= AIRoleAssistant
}

// String 返回角色的协议名，与 LLM 侧的 role 字段一致。
//
// 未定义取值返回空串而不是 panic 或某个默认角色：把脏数据映射成
// "user" 会让它被当作用户输入重新发给模型，那比丢掉它更危险。
func (r AIMessageRole) String() string {
	switch r {
	case AIRoleSystem:
		return "system"
	case AIRoleUser:
		return "user"
	case AIRoleAssistant:
		return "assistant"
	default:
		return ""
	}
}

// AIMessage 列宽常量；写入点按它们截断（理由同 AIConversation）。
const (
	AIMessageProviderLen     = 32
	AIMessageModelLen        = 64
	AIMessageFinishReasonLen = 32
)

// AIMessage 是会话中的一条消息。
//
// 它是 AIConversation 聚合的内部实体，只经 repository/aiconversation 读写。
type AIMessage struct {
	BaseID

	// ConversationID 是所属会话。
	ConversationID int64 `gorm:"column:conversation_id;not null;index:idx_ai_messages_conversation,priority:1;comment:会话ID"`
	// UserID 冗余归属人。
	//
	// 冗余的理由是**越权校验与按用户清理**：没有它，"这条消息是不是这个用户的"
	// 必须联表回 ai_conversations，而那是每条消息读取都要付的代价；
	// 按用户批量删除个人数据时同理。
	UserID int64 `gorm:"column:user_id;not null;index:idx_ai_messages_user;comment:归属用户ID"`

	Role AIMessageRole `gorm:"column:role;type:tinyint(1);not null;comment:角色 1-system 2-user 3-assistant"`
	// Content 是消息正文。
	//
	// mediumtext（16MB）而不是 text（64KB）：长文档粘贴 + 长回复很容易越过
	// 64KB，而严格 sql_mode 下那是一次整条写入失败——发生在模型已经答完、
	// 用户已经看到内容之后，历史里却什么都没留下。
	Content string `gorm:"column:content;type:mediumtext;not null;comment:消息正文"`
	// Reasoning 是模型的思考过程（DeepSeek-R1 这类推理模型才有），可为空。
	Reasoning string `gorm:"column:reasoning;type:mediumtext;comment:推理过程"`

	// Provider / Model 是产出这条消息的厂商与模型；user 消息为空。
	Provider string `gorm:"column:provider;size:32;not null;default:'';comment:厂商"`
	Model    string `gorm:"column:model;size:64;not null;default:'';comment:模型"`

	// 用量按条记录，供成本归因下钻到单条回复。int 足够：单次调用的
	// token 数受模型上下文窗口约束，不可能溢出 32 位。
	PromptTokens     int `gorm:"column:prompt_tokens;not null;default:0;comment:输入token"`
	CompletionTokens int `gorm:"column:completion_tokens;not null;default:0;comment:输出token"`
	TotalTokens      int `gorm:"column:total_tokens;not null;default:0;comment:总token"`

	// FinishReason 是本次生成的终止原因：stop / length / content_filter /
	// aborted（客户端中途断开，已生成部分仍然落库）等。
	//
	// 必须存：一个被 length 截断的回答与一个正常结束的回答在正文上看不出
	// 区别，而用户报"回答不完整"时，这是唯一能分辨的依据。
	FinishReason string `gorm:"column:finish_reason;size:32;not null;default:'';comment:终止原因"`

	BaseTimeField
}

// TableName 指定表名。
func (m *AIMessage) TableName() string {
	return "ai_messages"
}
