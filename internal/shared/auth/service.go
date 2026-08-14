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

// RoleVerifier 定义角色判定能力，实现方查询**权威角色源**。
//
// 与 UserVerifier / VIPVerifier 同一契约：仓储故障必须是 error 而非 false。
// 把"角色库挂了"折叠成"没有权限"，会让一次基础设施故障表现为全体管理员
// 突然失去权限，而客户端收到的是一个正常的 403——既不会重试，也不会触发告警。
//
// **实现必须查权威源，不得仅采信令牌里的角色声明**：令牌的寿命由签发时决定，
// 其中的角色反映的是签发那一刻的事实。仅采信它等于把一次签发当成持续有效的
// 高权限——一个被撤销管理员权限的账号，在令牌自然过期前仍然畅通无阻，
// 而系统里没有任何地方能看出这件事（见 Principal.Roles 的说明）。
//
// 只暴露 HasRole 而不是 ListRoles：判定只需要一个布尔，而 ListRoles 会把
// 可能很大的角色集合搬进内存，并诱导每个调用点各自决定缓存策略。
type RoleVerifier interface {
	HasRole(ctx context.Context, userID int64, role string) (bool, error)
}
