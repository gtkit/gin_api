package application

import (
	"strings"

	"github.com/gtkit/gin-api/internal/models"
)

// maxMessageRunes 是单条提问的字符上限。
//
// 它不是"模型能接受多长"——那由具体模型的上下文窗口决定，各家不同且随版本变化，
// 本层无从得知。它是一道**在付钱之前**就能做出的判断：越过这个量级的输入几乎
// 必然被上游以 context_length 拒绝，而那次拒绝是要计费的（多数厂商按已提交的
// 输入 token 计价）。宁可在本地拒绝并给出可执行的提示。
//
// 取 3 万字符：主流模型 128K token 窗口下，中文约 2 字符/token，留出历史与
// 回复的余量后，单条提问到这个量级已经不合理。
const maxMessageRunes = 30000

// normalizeMessage 归一化并校验本轮提问。
//
// 去首尾空白后判空而不是直接判空串：一条只有空格和换行的"提问"发给模型，
// 得到的是一次真实计费的无意义回复。
func normalizeMessage(raw string) (string, error) {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return "", ErrEmptyMessage
	}
	if len([]rune(msg)) > maxMessageRunes {
		return "", ErrContextTooLong
	}
	return msg, nil
}

// buildTitle 由首轮提问生成会话标题。
//
// 按 rune 截断而不是按 byte：按 byte 截会把一个多字节字符切成两半，
// 存进 utf8mb4 列时要么报错要么留下一个替换字符。
//
// 换行折成空格：标题在列表里是单行展示的，保留换行只会让它显示成一串空白。
func buildTitle(message string) string {
	title := strings.Join(strings.Fields(message), " ")
	return truncateRunes(title, models.AIConversationTitleLen)
}

// truncateRunes 按字符数截断。
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

// assembleMessages 组装发给模型的消息序列：系统提示词 → 历史 → 本轮提问。
//
// 历史由调用方按条数取好（仓储只取最近 N 条），本函数只负责拼装与过滤，
// 不再做二次裁剪——两处都裁会让"到底带了多少历史"取决于两个常量的相对大小，
// 而那种关系没有任何地方写着。
//
// 过滤掉三类历史消息，每一类都有具体理由：
//
//	空正文       ——  一次被中途放弃、什么都没生成的流会留下空的 assistant 记录。
//	                把它回传给模型，多数厂商直接判为非法请求。
//	未知角色     ——  枚举之外的取值只可能来自脏数据；映射成 user 会让它被当作
//	                用户输入重新发出去，那比丢掉它危险得多。
//	system 角色  ——  系统提示词由配置在每轮重新拼装，是**当前**的策略。历史里
//	                固化的那份可能是旧策略，两份叠加会让模型收到互相矛盾的指令。
func assembleMessages(systemPrompt string, history []*models.AIMessage, question string) []ChatMessage {
	messages := make([]ChatMessage, 0, len(history)+2)

	if prompt := strings.TrimSpace(systemPrompt); prompt != "" {
		messages = append(messages, ChatMessage{Role: RoleSystem, Content: prompt})
	}

	for _, msg := range history {
		if msg == nil || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		role, ok := chatRole(msg.Role)
		if !ok {
			continue
		}
		messages = append(messages, ChatMessage{Role: role, Content: msg.Content})
	}

	return append(messages, ChatMessage{Role: RoleUser, Content: question})
}

// chatRole 把持久化角色映射到协议角色；ok=false 表示该条不应进入上下文。
func chatRole(role models.AIMessageRole) (Role, bool) {
	switch role {
	case models.AIRoleUser:
		return RoleUser, true
	case models.AIRoleAssistant:
		return RoleAssistant, true
	case models.AIRoleSystem:
		// 见 assembleMessages 的说明：历史里的系统提示词不回放。
		return "", false
	default:
		return "", false
	}
}
