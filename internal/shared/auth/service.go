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

// VIPVerifier 定义会员中间件需要的 VIP 校验能力。
// 该接口只暴露“当前用户是否具备有效会员资格”的结果，
// 具体会员状态字段、到期时间判断等细节由实现方自行封装。
type VIPVerifier interface {
	ValidateVIP(ctx context.Context, userID int64) bool
}
