package models

import (
	"time"

	"github.com/gtkit/json"
)

// UserStatus 表示用户状态。
type UserStatus int8

// VipStatus 表示会员状态。
type VipStatus int8

// UserGender 表示用户性别。
type UserGender int8

type User struct {
	BaseID

	Nickname string     `gorm:"column:nickname;size:50;not null;default:'';comment:用户昵称"`
	Gender   UserGender `gorm:"column:gender;type:tinyint(1);not null;default:0;comment:性别 0-未知 1-男 2-女"`
	Avatar   string     `gorm:"column:avatar;size:255;not null;default:'';comment:头像URL"`
	Phone    string     `gorm:"column:phone;size:20;uniqueIndex;comment:手机号"`
	Email    *string    `gorm:"column:email;size:100;uniqueIndex;comment:邮箱"`
	Password string     `gorm:"column:password;size:255;not null;comment:密码"`

	Status UserStatus `gorm:"column:status;type:tinyint(1);index;not null;default:1;comment:用户状态 1-正常 2-禁用 3-删除"`

	// 以下字段是**存量业务的形状，不是通用母版该有的东西**。
	//
	// 会员资格与公众号关注属于特定业务领域。派生项目应当按自身领域重塑
	// 这张表，而不是默认继承它们——继承的代价不只是几个用不到的列：
	// entitlement.vip authorizer、VIP 相关的 dto 字段与迁移都会跟着留下来，
	// 而它们会让人以为这套判定是母版的通用能力。
	//
	// 删除步骤见 SCAFFOLD.md 的「示例 — 可按需删除」。
	VipStatus   VipStatus  `gorm:"column:vip_status;type:tinyint(1);index;not null;default:0;comment:会员状态 0-非会员 1-会员 2-已过期 3-已取消"`
	VipLevel    int8       `gorm:"column:vip_level;type:tinyint(1);index;not null;default:0;comment:会员级别"`
	VipExpireAt *time.Time `gorm:"column:vip_expire_at;type:timestamp;comment:会员到期时间"`
	Follow      int8       `gorm:"column:follow;type:tinyint(1);index;not null;default:0;comment:是否关注公众号 0-未关注 1-关注"`

	// Profile 表示用户与扩展资料表的一对一关联。
	// 外键实际落在 user_profiles.user_id，上层若需要联表读取，可直接：
	// db.Preload("Profile").First(&user, id)
	// 这里不向客户端直接暴露，避免接口层无意带出递归结构。
	Profile *UserProfile `gorm:"foreignKey:UserID;references:ID" json:"-"`

	BaseTimeField
}

const (
	UserStatusNormal UserStatus = iota + 1
	UserStatusDisable
	UserStatusDeleted
)

const (
	VipStatusInactive VipStatus = iota
	VipStatusActive
	VipStatusExpired
	VipStatusCanceled
)

const (
	UserGenderUnknown UserGender = iota
	UserGenderMale
	UserGenderFemale
)

func (user *User) TableName() string {
	return "users"
}

// MarshalBinary 实现 encoding.BinaryMarshaler：go-redis 把 struct 作为命令参数时只接受该接口。
func (user *User) MarshalBinary() ([]byte, error) {
	return json.Marshal(user)
}

// UnmarshalBinary 实现 encoding.BinaryUnmarshaler，与 MarshalBinary 配对供 Scan 使用。
func (user *User) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, user)
}
