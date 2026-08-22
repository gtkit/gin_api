package dto

// Gender 是对外暴露的性别取值。
//
// 用具名类型而不是裸 int8：契约因此可以声明合法取值集合，而客户端能据此
// 生成类型安全的常量。裸整数只能声明成"某个整数"，调用方无从判断 3 是否合法。
//
// **承诺由代码兑现**：构造响应时经 NormalizeGender 收敛，未知值一律落到
// GenderUnknown。此前契约声称 gender ∈ [0,2] 而代码里没有任何东西保证它——
// 那是一条只在文档里成立的承诺，比不承诺更糟，因为客户端会据此写死枚举分支。
type Gender int8

const (
	// GenderUnknown 是未知或未设置。
	GenderUnknown Gender = iota
	// GenderMale 男。
	GenderMale
	// GenderFemale 女。
	GenderFemale
)

// NormalizeGender 把任意底层取值收敛到合法枚举。
//
// 数据库里可能存在历史脏数据或将来新增而本版本不认识的取值。直接透传会让
// 响应违反契约声明的枚举，而客户端的 switch 会落到 default 或直接报错；
// 收敛到"未知"是唯一对老客户端安全的处置。
func NormalizeGender(value int8) Gender {
	switch g := Gender(value); g {
	case GenderMale, GenderFemale:
		return g
	case GenderUnknown:
		return GenderUnknown
	default:
		return GenderUnknown
	}
}

type UserProfileResponse struct {
	ID        int64  `json:"id"`
	Nickname  string `json:"nickname"`
	Gender    Gender `json:"gender"`
	Avatar    string `json:"avatar"`
	Phone     string `json:"phone"`
	Status    int8   `json:"status"`
	VIPStatus int8   `json:"vip_status"`
	VIPLevel  int8   `json:"vip_level"`
}

// UserUpdateRequest 资料更新请求。字段是否用指针按「零值是否是合法目标值」决定：
//   - Gender 的 0 可能是合法取值（如"保密"），必须区分「未传(nil)」与「传 0」→ *int8；
//   - NickName/Avatar 的空串不是合法目标（空头像/空昵称无意义，Avatar 还要过 URL 校验），
//     "传空串" 与 "未传" 都视为不更新 → 值类型 + omitempty 即可，无需指针。
//
// Phone 不在此处更新：手机号变更需走独立的验证码校验流程。
//
// Avatar 的两条约束各自不可省：
//
//   - `max=255` 与 `users.avatar` 的列宽对齐。契约放行一个数据库存不下的值，
//     只会把入口可拒绝的请求变成写入失败——严格模式下那是一个未被错误映射覆盖的
//     驱动错误，于是一次纯客户端输入错误被记成 500 与 error 级日志。
//   - `http_url` 而不是 `url`：后者**不限 scheme**，实测接受 `javascript:`、`data:`、
//     `file:`、`vbscript:`。头像 URL 入库后由任意消费端渲染成链接即是存储型 XSS
//     载体，而入口是唯一能以零成本拒绝它的位置。
//     注意 `http_url` 不在 validator 的中文翻译包里，它的中文消息由
//     internal/bootstrap/validation.go 的注册位补上。
type UserUpdateRequest struct {
	NickName string `json:"nick_name" form:"nick_name" binding:"omitempty,max=50"`
	Gender   *int8  `json:"gender" form:"gender" binding:"omitempty,min=0,max=2"`
	Avatar   string `json:"avatar" form:"avatar" binding:"omitempty,http_url,max=255"`
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
