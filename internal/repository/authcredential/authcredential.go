// Package authcredential 保管本地认证需要的用户凭据访问。
//
// 与 repository/user 分开：那个仓储服务于资料读写，本包只碰凭据列。分开的收益
// 在删除时最明显——使用者改用外部身份提供方时，整包连同 module/auth 一起删掉，
// 不必从一个混合仓储里挑出哪些方法是认证专用的。
package authcredential

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/repository/dbtx"
)

// Repository 是凭据仓储。
type Repository struct {
	db *gorm.DB
}

// New 构造仓储。
func New(db *gorm.DB) *Repository { return &Repository{db: db} }

// mdbCtx 与其它仓储一致：事务内自动用事务句柄。
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB {
	return dbtx.DB(ctx, r.db)
}

// Credential 是登录所需的最小凭据集合。
//
// 刻意不返回整个 models.User：登录只需要这三项，而把完整用户对象交给认证
// 逻辑，会让"顺手用一下别的字段"变得自然，凭据校验因此悄悄依赖上业务字段。
type Credential struct {
	UserID       int64
	PasswordHash string
	// Active 表示账号当前是否可登录。
	//
	// 单独一个布尔而不是把 status 透出去：认证要回答的是"能不能登录"，
	// 而 status 的取值含义属于业务，两者一旦耦合，业务加一个状态值就要
	// 回来改认证逻辑。
	Active bool
}

// FindByPhone 按手机号查询凭据。
//
// found=false 表示账号不存在（非错误）。**调用方必须在此时仍执行一次密码
// 比对**，否则响应时间会泄漏手机号是否已注册——见 credential.CompareWithDummy。
func (r *Repository) FindByPhone(ctx context.Context, phone string) (cred Credential, found bool, err error) {
	var user models.User
	err = r.mdbCtx(ctx).
		Select("id", "password", "status").
		Where("phone = ?", phone).
		First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Credential{}, false, nil
	}
	if err != nil {
		// 不带上手机号：错误会进日志，而手机号是个人信息。用户 ID 此时也拿不到，
		// 因此这里只说明操作本身。
		return Credential{}, false, fmt.Errorf("find credential by phone: %w", err)
	}

	return Credential{
		UserID:       user.ID,
		PasswordHash: user.Password,
		Active:       user.Status == models.UserStatusNormal,
	}, true, nil
}

// ErrPhoneTaken 表示手机号已被注册。
//
// 单独定义让 application 能把它判成 409 而不是 500：这是一个正常的业务结果，
// 不是故障。
var ErrPhoneTaken = errors.New("authcredential: phone is already registered")

// Create 建号。
//
// 手机号重复由**数据库唯一索引**判定，而不是先查后插：两个并发注册会同时
// 通过"查一下不存在"的检查，然后一个成功一个撞索引——查询只是把竞态窗口
// 变窄，没有消除它。这里直接插入并把唯一键冲突翻译成 ErrPhoneTaken。
func (r *Repository) Create(ctx context.Context, phone, passwordHash, nickname string) (int64, error) {
	user := models.User{
		Phone:    phone,
		Password: passwordHash,
		Nickname: nickname,
		Status:   models.UserStatusNormal,
	}

	if err := r.mdbCtx(ctx).Create(&user).Error; err != nil {
		if isDuplicateKey(err) {
			return 0, ErrPhoneTaken
		}
		return 0, fmt.Errorf("create user: %w", err)
	}
	return user.ID, nil
}

// UpdatePassword 改密。
func (r *Repository) UpdatePassword(ctx context.Context, userID int64, passwordHash string) error {
	result := r.mdbCtx(ctx).
		Model(&models.User{}).
		Where("id = ?", userID).
		Update("password", passwordHash)
	if result.Error != nil {
		return fmt.Errorf("update password for user %d: %w", userID, result.Error)
	}
	// 影响行数为 0 意味着账号不存在或已被删除。静默成功会让调用方以为密码
	// 已经改了——而下一次登录仍然接受旧密码。
	if result.RowsAffected == 0 {
		return fmt.Errorf("update password for user %d: no such user", userID)
	}
	return nil
}

// mysqlDuplicateEntry 是 MySQL 的唯一键冲突错误码。
const mysqlDuplicateEntry = 1062

// isDuplicateKey 判断错误是否为唯一键冲突。
//
// **两条判断都要**，不是冗余：
//
// gorm.ErrDuplicatedKey 只在连接开启了 TranslateError 时才会出现（见
// platform/data 的 Open）。那是个进程级配置，而本包的正确性不该依赖
// 另一处配置保持不变——它一旦被去掉，重复注册会静默地从 409 变成 500，
// 且没有任何测试会失败。
//
// 反过来只匹配错误码也不行：错误码是 MySQL 特有的，换库就失效。
func isDuplicateKey(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateEntry
}
