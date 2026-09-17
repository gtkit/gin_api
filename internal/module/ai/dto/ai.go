// Package dto 是 AI 对话模块的对外数据契约。
package dto

import (
	"time"

	"github.com/gtkit/gin-api/internal/pkg/paging"
)

// ChatRequest 是一次对话请求，流式与非流式共用。
//
// 共用是对的：两条路由的**输入**完全一样，差别只在响应形态。拆成两个结构体
// 会让"给对话加一个参数"变成要改两处，而漏改的那一处不会有任何编译错误。
type ChatRequest struct {
	// ConversationID 为 0 或缺省表示开启新会话。
	ConversationID int64 `json:"conversation_id" binding:"omitempty,gte=1" example:"1"`

	// Message 是本轮提问。
	//
	// 上限 30000 与 application 的 maxMessageRunes 同值：契约放行一个业务层
	// 必然拒绝的值，只会把一次可以在入口挡下的请求变成一次更晚、更贵的拒绝。
	//
	// 注意 binding 的 max 对 string 按 **rune 数**计（validator 用
	// utf8.RuneCountInString），与业务层的判据一致，中文不会被按字节误判。
	Message string `json:"message" binding:"required,max=30000" example:"用一句话介绍 Go 语言"`

	// Provider 为空表示用配置里的默认厂商。
	//
	// 不在这里用 oneof 钉死取值：可选集合来自运行期配置（填了 api_key 的那些），
	// 而 binding tag 是编译期常量。写死一份会与配置分叉——加一家厂商却忘了
	// 改 tag，那家就永远调不到，且报的是"参数非法"。合法性由 application
	// 判定，未启用的名字得到 ErrUnknownProvider。
	Provider string `json:"provider" binding:"omitempty,max=32" example:"deepseek"`

	// Model 为空表示用该厂商的默认模型。
	Model string `json:"model" binding:"omitempty,max=64" example:"deepseek-chat"`
}

// UsageResponse 是一次调用的 token 用量。
type UsageResponse struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse 是非流式对话的响应。
type ChatResponse struct {
	ConversationID int64 `json:"conversation_id"`
	// Title 是会话标题；首轮由提问自动生成。
	Title     string `json:"title"`
	MessageID int64  `json:"message_id"`
	Content   string `json:"content"`
	// Reasoning 是推理模型的思考过程，非推理模型为空。
	Reasoning string `json:"reasoning,omitempty"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	// FinishReason 取 stop / length / content_filter / aborted 等。
	//
	// 必须回传：一个被 length 截断的回答与一个完整的回答在正文上看不出区别，
	// 客户端要据此决定是否提示"回答被截断"。
	FinishReason string        `json:"finish_reason"`
	Usage        UsageResponse `json:"usage"`
}

// StreamDelta 是一个增量文本片段（event: delta）。
//
// 流上刻意**没有** meta 这类前置事件：conversation_id 要等落库才存在，
// provider/model 也只有调用返回后才是真实取值。提前发一条"预告"意味着
// 它可能与最终事实不符，而客户端多半会先把预告当真。全部放进 done。
type StreamDelta struct {
	Content string `json:"content"`
}

// StreamDone 是流的收尾事件（event: done），**已落库**时发出。
//
// Partial 区分两种收尾：正常结束（false）与中途被打断但已保存部分（true）。
// 两者都发 done 而不是让后者走 error，是因为客户端在这两种情况下的处置相同：
// 拿 conversation_id 继续聊。差别只是要不要提示"回答不完整"。
type StreamDone struct {
	ConversationID int64  `json:"conversation_id"`
	Title          string `json:"title"`
	MessageID      int64  `json:"message_id"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	FinishReason   string `json:"finish_reason"`
	// Partial 为真表示生成被中断，已生成的部分仍然落了库。
	Partial bool          `json:"partial"`
	Usage   UsageResponse `json:"usage"`
}

// StreamError 是流失败且**没有任何内容落库**时的事件（event: error）。
//
// 响应头一旦发出就不能再改状态码，因此起流之后的失败只能经事件体表达。
// 客户端必须把它当作终态处理——它之后不会再有 done。
//
// 已生成部分并落了库的情形走 StreamDone{Partial: true}，不走这里。
type StreamError struct {
	Message string `json:"message"`
}

// ConversationListRequest 是会话列表请求。
type ConversationListRequest struct {
	Page     int `form:"page" binding:"omitempty,gte=1"`
	PageSize int `form:"page_size" binding:"omitempty,gte=1,lte=100"`
	// Sort 的 oneof 是可排序键白名单，与 repository 的 listSortMapping 同源；
	// 新增可排序字段时两处同步更新。非法键在绑定层拒绝（422），
	// 而不是静默回退——静默回退会把客户端拼错的键藏起来。
	Sort  string `form:"sort" binding:"omitempty,oneof=id created_at last_message_at"`
	Order string `form:"order" binding:"omitempty,oneof=asc desc"`
}

// Query 把列表请求翻译成中立分页入参。
func (r ConversationListRequest) Query() paging.Query {
	return paging.Query{Page: r.Page, PageSize: r.PageSize, Sort: r.Sort, Order: r.Order}
}

// MessageListRequest 是会话详情里的消息分页请求。
type MessageListRequest struct {
	Page     int    `form:"page" binding:"omitempty,gte=1"`
	PageSize int    `form:"page_size" binding:"omitempty,gte=1,lte=100"`
	Sort     string `form:"sort" binding:"omitempty,oneof=id created_at"`
	Order    string `form:"order" binding:"omitempty,oneof=asc desc"`
}

// Query 把消息列表请求翻译成中立分页入参。
func (r MessageListRequest) Query() paging.Query {
	return paging.Query{Page: r.Page, PageSize: r.PageSize, Sort: r.Sort, Order: r.Order}
}

// RenameRequest 是改标题请求。
type RenameRequest struct {
	// Title 上限 120 与 ai_conversations.title 列宽一致：契约放行一个数据库
	// 存不下的值，只会把一个本可以在入口拒绝的请求变成一次写入失败。
	Title string `json:"title" binding:"required,max=120" example:"Go 语言入门"`
}

// 时间字段用 omitzero 而不是 omitempty：后者对 struct 类型永不触发，零值时间
// 照样输出 "0001-01-01T00:00:00Z"——标注写了却不生效。omitzero（Go 1.24 起）
// 按 IsZero() 判定。
//
// ConversationResponse 是会话摘要。
type ConversationResponse struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// MessageCount 含 user 与 assistant 两侧。
	MessageCount     int       `json:"message_count"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	LastMessageAt    time.Time `json:"last_message_at,omitzero"`
	CreatedAt        time.Time `json:"created_at,omitzero"`
	UpdatedAt        time.Time `json:"updated_at,omitzero"`
}

// MessageResponse 是一条消息。
type MessageResponse struct {
	ID               int64     `json:"id"`
	Role             string    `json:"role"`
	Content          string    `json:"content"`
	Reasoning        string    `json:"reasoning,omitempty"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	FinishReason     string    `json:"finish_reason"`
	CreatedAt        time.Time `json:"created_at,omitzero"`
}

// ConversationListResponse 是分页后的会话列表。
type ConversationListResponse struct {
	Items  []ConversationResponse `json:"items"`
	Paging paging.Paging          `json:"paging"`
}

// ConversationDetailResponse 是会话详情：会话头 + 一页消息。
type ConversationDetailResponse struct {
	Conversation ConversationResponse `json:"conversation"`
	Messages     []MessageResponse    `json:"messages"`
	Paging       paging.Paging        `json:"paging"`
}

// ProviderResponse 是一家已启用厂商。
type ProviderResponse struct {
	Name string `json:"name"`
	// Model 是该厂商生效的默认模型。
	Model string `json:"model"`
	// Default 标记它是否为不指定厂商时使用的那一家。
	Default bool `json:"default"`
}
