package models

import "github.com/gtkit/json"

// UserDeactivation 用户注销记录表。
//
// 注销采用方案 A：在 users 表上脱敏改写并将 status 置为 UserStatusDeleted，
// 同时在本表保留注销前的关键身份字段，用于：
//  1. 审计与合规留痕（谁、何时、为何注销）；
//  2. 申诉恢复（用户后续要求恢复账号时核对身份）；
//  3. 风控复核（同一手机/邮箱被反复注销的异常识别）。
//
// 原 Phone 在 users 表中已被改写为 `DEL_{id}_{ts}`，原 Email 已置 NULL，
// 因此原始值仅保存在本表。
type UserDeactivation struct {
	BaseID

	UserID           int64   `gorm:"column:user_id;index;not null;comment:被注销的用户ID"`
	OriginalPhone    string  `gorm:"column:original_phone;size:20;not null;default:'';comment:注销前手机号"`
	OriginalEmail    *string `gorm:"column:original_email;size:100;comment:注销前邮箱"`
	OriginalNickname string  `gorm:"column:original_nickname;size:50;not null;default:'';comment:注销前昵称"`
	Reason           string  `gorm:"column:reason;size:255;not null;default:'';comment:注销原因（用户填写）"`
	Channel          string  `gorm:"column:channel;size:32;not null;default:'';index;comment:注销渠道（app/h5/admin 等）"`
	OperatorIP       string  `gorm:"column:operator_ip;size:64;not null;default:'';comment:发起注销的来源IP"`

	BaseTimeField
}

// TableName 指定表名。
func (u *UserDeactivation) TableName() string {
	return "user_deactivations"
}

// MarshalBinary 实现 encoding.BinaryMarshaler：go-redis 把 struct 作为命令参数时只接受该接口。
func (u *UserDeactivation) MarshalBinary() ([]byte, error) {
	return json.Marshal(u)
}

// UnmarshalBinary 实现 encoding.BinaryUnmarshaler，与 MarshalBinary 配对供 Scan 使用。
func (u *UserDeactivation) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, u)
}
