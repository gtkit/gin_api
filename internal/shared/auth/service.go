package auth

import "context"

// ClientRole 是客户端 JWT 角色标识（领域常量，不依赖 gin）。
const ClientRole = "user"

// UserVerifier 定义鉴权中间件需要的用户校验能力。
// 返回 (false, err) 表示校验因基础设施故障（如 DB 不可用）无法完成，
// 调用方应返回 503 而非 401；(false, nil) 才表示用户确实不可用。
type UserVerifier interface {
	ValidateUser(ctx context.Context, userID int64) (bool, error)
}

// VIPVerifier 定义会员资格校验能力。
//
// 返回 (bool, error) 而不是裸 bool，与 UserVerifier 保持同一契约：
// 仓储故障与"确实不是会员"是两件不同的事。裸 bool 会把"数据库挂了"折叠成
// "不是会员"，于是一次基础设施故障表现为全体会员突然失去权益，
// 而客户端看到的是一个正常的业务拒绝，既不会重试也不会触发告警。
//
// 调用方应把 error 映射为 503。
type VIPVerifier interface {
	ValidateVIP(ctx context.Context, userID int64) (bool, error)
}
