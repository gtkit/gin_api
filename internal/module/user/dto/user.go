package dto

type UserProfileResponse struct {
	ID        int64  `json:"id"`
	Nickname  string `json:"nickname"`
	Gender    int8   `json:"gender"`
	Avatar    string `json:"avatar"`
	Phone     string `json:"phone"`
	Status    int8   `json:"status"`
	VIPStatus int8   `json:"vip_status"`
	VIPLevel  int8   `json:"vip_level"`
}

// UserUpdateRequest 资料更新请求。字段是否用指针按「零值是否是合法目标值」决定：
//   - Gender 的 0 可能是合法取值（如"保密"），必须区分「未传(nil)」与「传 0」→ *int8；
//   - NickName/Avatar 的空串不是合法目标（空头像/空昵称无意义，Avatar 还要过 url 校验），
//     "传空串" 与 "未传" 都视为不更新 → 值类型 + omitempty 即可，无需指针。
//
// Phone 不在此处更新：手机号变更需走独立的验证码校验流程。
type UserUpdateRequest struct {
	NickName string `json:"nick_name" form:"nick_name" binding:"omitempty,max=50"`
	Gender   *int8  `json:"gender" form:"gender" binding:"omitempty,min=0,max=2"`
	Avatar   string `json:"avatar" form:"avatar" binding:"omitempty,url"`
}

// UserDeactivateRequest 用户注销请求体。
//
// 注销不可逆：客户端调用前需自行做二次确认。
//   - Reason  可选，用于风控与体验改进分析。
//   - Channel 必填，记录请求来源（app/h5/admin 等），用于审计与分渠道统计。
type UserDeactivateRequest struct {
	Reason  string `json:"reason" form:"reason" binding:"omitempty,max=255"`
	Channel string `json:"channel" form:"channel" binding:"required,max=32"`
}
