// Package dto 是认证模块的对外数据契约。
package dto

// RegisterRequest 是注册请求。
type RegisterRequest struct {
	// Phone 是登录标识。
	//
	// 长度上限 20 与 users.phone 列宽一致：契约放行一个数据库存不下的值，
	// 只会把一个本可以在入口拒绝的请求变成一次写入失败。
	Phone string `json:"phone" binding:"required,max=20" example:"13800000000"`

	// Password 是明文口令，仅用于本次请求。
	//
	// 下限 8 是入口处的最低要求。上限 72 来自 bcrypt 的处理上限——
	// 超过它的部分不参与哈希，放行只会让用户以为自己设了一个更长的口令。
	Password string `json:"password" binding:"required,min=8,max=72" example:"a-strong-password"`

	// Nickname 可选，留空时由业务决定默认值。
	Nickname string `json:"nickname" binding:"max=50" example:"新用户"`
}

// LoginRequest 是登录请求。
//
// **不复用 RegisterRequest**：登录不该对口令长度设下限。用户的存量口令可能
// 是在旧规则下设置的，一条 min=8 会把他们挡在门外，而那与凭据是否正确无关。
type LoginRequest struct {
	Phone    string `json:"phone" binding:"required,max=20" example:"13800000000"`
	Password string `json:"password" binding:"required,max=72" example:"a-strong-password"`
}

// RefreshRequest 是刷新请求。
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required,max=512" example:"<refresh token>"`
}

// LogoutRequest 是登出请求。
//
// RefreshToken 可选：只带访问令牌也应当能登出，只是那条刷新链要等自然过期。
// 强制要求它会让"客户端弄丢了刷新令牌"变成"无法登出"。
type LogoutRequest struct {
	RefreshToken string `json:"refresh_token" binding:"max=512" example:"<refresh token>"`
}

// TokenResponse 是一次成功认证的产出。
type TokenResponse struct {
	AccessToken string `json:"access_token" example:"<jwt>"`
	// RefreshToken 每次刷新都会变：旧的在服务端已作废，客户端必须换成这个新值。
	RefreshToken string `json:"refresh_token" example:"<refresh token>"`
	// ExpiresIn 是访问令牌剩余秒数，供客户端安排刷新时机。
	//
	// 给秒数而不是绝对时刻：客户端与服务端的时钟未必一致，而"还能用多久"
	// 不受时钟偏差影响。
	ExpiresIn int64 `json:"expires_in" example:"900"`
	// TokenType 固定为 Bearer，便于客户端直接拼装请求头。
	TokenType string `json:"token_type" example:"Bearer"`
}
