// Package user_profile 模型
package models

import (
	"time"
)

// UserProfile 用户扩展信息表。
type UserProfile struct {
	BaseID
	UserID int64 `gorm:"column:user_id;uniqueIndex;not null;comment:用户ID"`

	// User 表示扩展资料回指主用户的一对一关联。
	// 这里声明 belongs-to 关系，便于后续直接：
	// db.Preload("User").First(&profile, id)
	// 真正的数据库外键约束仍然依赖 migration SQL 落库。
	User *User `gorm:"foreignKey:UserID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`

	// 设备信息
	RegisterIP    string     `gorm:"column:register_ip;size:45;not null;default:'';comment:注册IP"`
	LastLoginIP   string     `gorm:"column:last_login_ip;size:45;not null;default:'';comment:最后登录IP"`
	LastLoginTime *time.Time `gorm:"column:last_login_time;type:timestamp;not null;comment:最后登录时间"`
	DeviceSystem  string     `gorm:"column:device_system;size:50;not null;default:'';comment:手机系统"`
	DeviceModel   string     `gorm:"column:device_model;size:100;not null;default:'';comment:手机型号"`
	DeviceID      string     `gorm:"column:device_id;size:100;index;not null;default:'';comment:设备识别码"`

	// 渠道与邀请信息
	Source     string `gorm:"column:source;size:50;not null;default:'direct';comment:用户来源"`
	ChannelID  int64  `gorm:"not null;index:idx_channel;comment:发布渠道ID"`
	InviteCode string `gorm:"column:invite_code;size:20;index;not null;default:'';comment:邀请码"`
	InvitedBy  uint64 `gorm:"column:invited_by;type:bigint(20);index;default:0;comment:邀请人ID"`

	// 元数据
	Remark    string `gorm:"column:remark;size:255;not null;default:'';comment:备注"`
	ExtraData string `gorm:"column:extra_data;type:json;comment:扩展JSON数据"`

	BaseTimeField
}

func (userProfile *UserProfile) TableName() string {
	return "user_profiles"
}
