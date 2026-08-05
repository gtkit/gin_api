// Package application 是本地认证的业务编排。
//
// 与其它模块一致：本层不认识 gorm、不认识 gin、不认识 Redis。凭据存取与令牌
// 存储都经接口注入，实现由装配层提供——这让登录/刷新/登出的逻辑可以在没有
// 任何外部依赖的情况下被完整测试，而那些逻辑恰恰是最需要被测到的。
package application

import (
	"context"
	"time"
)

// CredentialRepository 是凭据存取。
type CredentialRepository interface {
	// FindByPhone 查询凭据；found=false 表示账号不存在。
	FindByPhone(ctx context.Context, phone string) (Credential, bool, error)
	// Create 建号并返回用户 ID；手机号已存在时返回 ErrPhoneTaken。
	Create(ctx context.Context, phone, passwordHash, nickname string) (int64, error)
}

// Credential 是登录所需的最小凭据集合。
//
// 在本层重新声明而不是引用仓储包的同名类型：application 不该 import
// repository——那是架构边界。字段少且稳定，重复一次的代价小于打破边界。
type Credential struct {
	UserID       int64
	PasswordHash string
	Active       bool
}

// PasswordHasher 生成与校验密码哈希。
type PasswordHasher interface {
	Hash(password string) (string, error)
	Compare(hashed, password string) error
	// CompareWithDummy 在账号不存在时消耗等量的时间，恒返回不匹配错误。
	CompareWithDummy(password string) error
}

// AccessIssuer 签发访问令牌。
type AccessIssuer interface {
	Issue(ctx context.Context, uid int64, roles ...string) (string, error)
}

// RefreshStore 保管刷新令牌。
type RefreshStore interface {
	Issue(ctx context.Context, uid int64) (string, error)
	// Consume 消费并轮换；检测到重用时撤销整族。
	Consume(ctx context.Context, token string) (uid int64, newToken string, err error)
	RevokeFamilyOfToken(ctx context.Context, token string) error
}

// RevocationList 记录被撤销的访问令牌。
type RevocationList interface {
	Revoke(ctx context.Context, tokenID string, expiresAt time.Time) error
}

// EventRecorder 记录认证事件的指标。
//
// 与 Auditor 分开：审计要的是"这一次发生了什么"（带用户 ID、可追溯），
// 指标要的是"过去十分钟发生了多少次"（不能带用户 ID，否则序列数随注册
// 用户数增长）。一个接口同时满足两者，必然在某一端妥协。
type EventRecorder interface {
	// RecordAuthEvent 记录一次认证事件。**不接受任何用户标识**。
	RecordAuthEvent(ctx context.Context, event string)
}

// 认证事件的取值集合。取值有限是硬要求：它们会成为指标标签。
const (
	EventLoginSucceeded = "login_succeeded"
	EventLoginFailed    = "login_failed"
	EventRefreshReused  = "refresh_reused"
	EventLogout         = "logout"
)

// Auditor 记录认证事件。
//
// 认证事件的审计价值高于普通业务操作：一次成功登录、一次重用检测、一次
// 撤销失败，都是安全排查的起点。分级与 worker 的 Auditor 同理——
// 失败与成功共用一个级别，告警规则就只能靠解析文本。
type Auditor interface {
	Auditf(format string, args ...any)
	Failf(format string, args ...any)
}
