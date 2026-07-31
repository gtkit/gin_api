package models

import (
	"strconv"
	"time"

	"github.com/gtkit/gin-api/internal/appmeta"
	"github.com/gtkit/gin-api/internal/pkg/paging"

	"github.com/gtkit/json"
	"gorm.io/gorm"
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

	Status      UserStatus `gorm:"column:status;type:tinyint(1);index;not null;default:1;comment:用户状态 1-正常 2-禁用 3-删除"`
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

type UserListPaging struct {
	List   []*User       `json:"list"`
	Paging paging.Paging `json:"paging"`
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

// GetStringID 返回字符串格式的 ID。
func (user *User) GetStringID() string {
	return strconv.Itoa(int(user.ID))
}

// MarshalBinary 将用户序列化为 JSON。
func (user *User) MarshalBinary() ([]byte, error) {
	return json.Marshal(user)
}

// UnmarshalBinary 将 JSON 反序列化为用户。
func (user *User) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, user)
}

// BeforeCreate 在创建用户前补齐默认头像。
func (user *User) BeforeCreate(tx *gorm.DB) error {
	if user.Avatar == "" {
		user.Avatar = getDefaultAvatar(user.Gender)
	}

	return nil
}

func getDefaultAvatar(_ UserGender) string {
	return appmeta.DefaultAvatarURL
}
