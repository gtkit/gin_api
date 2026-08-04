package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gtkit/gin-api/internal/models"
)

var (
	// ErrUserNotFound 表示用户不存在或已被软删除。
	ErrUserNotFound = errors.New("user not found")
	// ErrAlreadyDeactivated 表示用户已是注销态，幂等返回。
	ErrAlreadyDeactivated = errors.New("user already deactivated")
)

// deactivatedNickname 注销后对客展示的占位昵称（业务文案，由 application 决定）。
const deactivatedNickname = "已注销用户"

// maxPhoneColumnLength 是 users.phone 的列宽，脱敏值必须落在其内。
//
// 与迁移和 model tag 保持一致。超出时 MySQL 严格模式直接拒绝写入，
// 整个注销事务回滚——用户点了注销却失败，而失败原因是一个纯粹的编码问题。
const maxPhoneColumnLength = 20

// anonymizedPhone 生成脱敏手机号。
//
// **用 36 进制而不是十进制**：十进制的 `DEL_<id>_<unix>` 在用户 ID 达到 6 位时
// 就是 21 字符，已经超出列宽——而这类溢出在小数据量的测试里永远碰不到，
// 只会在用户量涨上去之后，以"注销功能突然对新用户失效"的形式出现。
//
// 36 进制把两个数字段各压缩约三成：ID 到万亿级、时间戳到下个世纪，
// 结果仍在 20 字符内。
//
// 长度检查是最后防线：真到了编码也压不住的规模，这里返回一个说清原因的错误，
// 好过让 MySQL 抛一个 "Data too long for column 'phone'"——后者要读到 SQL 层
// 才能明白发生了什么。
func anonymizedPhone(userID int64, at time.Time) (string, error) {
	value := "DEL_" + strconv.FormatInt(userID, 36) + "_" + strconv.FormatInt(at.Unix(), 36)
	if len(value) > maxPhoneColumnLength {
		return "", fmt.Errorf(
			"anonymized phone %q is %d chars, exceeding the %d-char phone column: "+
				"widen the column before user ids grow further",
			value, len(value), maxPhoneColumnLength)
	}
	return value, nil
}

// DeactivateInput 注销动作的全部上下文。
//
//   - UserID    必填，待注销用户。
//   - Reason    可选，用户填写的注销原因。
//   - Channel   必填，请求渠道（app/h5/admin 等），用于审计与分渠道统计。
//   - OperatorIP 来源 IP，由 handler 从请求里取。
type DeactivateInput struct {
	UserID     int64
	Reason     string
	Channel    string
	OperatorIP string
}

// Deactivate 执行用户注销（方案 A：脱敏改写 + 写注销记录 + 软删除）。
//
// 事务边界在此编排（经 TxManager.Do），事务内步骤：
//  1. LockByID 以 SELECT ... FOR UPDATE 锁定用户行，规避并发重复注销；
//  2. 检查未被注销（status != Deleted），否则幂等返回 ErrAlreadyDeactivated；
//  3. 写 user_deactivations 记录（保留 phone/email/nickname 原值，供审计/申诉）；
//  4. AnonymizeForDeactivation 脱敏改写 + 软删 users 行（脱敏后的 phone/昵称由本层决定）；
//  5. SoftDeleteProfile 软删 user_profiles 关联行。
//
// 任一步失败整事务回滚。SQL 细节下沉到 repository，本层只做业务编排。
func (s *Service) Deactivate(ctx context.Context, in DeactivateInput) error {
	if in.UserID <= 0 {
		return ErrUserNotFound
	}

	// 审计分两层，边界不同：
	//   业务事实（user_deactivations 记录）必须与注销**原子提交**——它是申诉与合规的依据，
	//     事务回滚了却留下一条"已注销"记录，比没有记录更糟。
	//   操作日志是 best-effort，且在**事务之外**打印：日志后端卡住时，
	//     若在事务内调用就会连带把数据库事务与行锁一起拖住，
	//     于是"日志系统抖动"升级成"注销接口全线阻塞"。日志失败绝不回滚业务。
	if err := s.tx.Do(ctx, func(ctx context.Context) error {
		user, found, err := s.repo.LockByID(ctx, in.UserID)
		if err != nil {
			return err
		}
		if !found {
			return ErrUserNotFound
		}
		if user.Status == models.UserStatusDeleted {
			return ErrAlreadyDeactivated
		}

		record := &models.UserDeactivation{
			UserID:           user.ID,
			OriginalPhone:    user.Phone,
			OriginalEmail:    user.Email,
			OriginalNickname: user.Nickname,
			Reason:           in.Reason,
			Channel:          in.Channel,
			OperatorIP:       in.OperatorIP,
		}
		if err := s.recorder.Create(ctx, record); err != nil {
			return fmt.Errorf("create deactivation record: %w", err)
		}

		// 脱敏 phone 释放原手机号唯一索引并保留可追溯前缀（业务策略）。
		anonymized, err := anonymizedPhone(user.ID, s.currentTime())
		if err != nil {
			return err
		}
		if err := s.repo.AnonymizeForDeactivation(ctx, user.ID, anonymized, deactivatedNickname); err != nil {
			return err
		}
		if err := s.repo.SoftDeleteProfile(ctx, user.ID); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return err
	}

	s.auditf("user %d deactivated via channel=%s reason_len=%d", in.UserID, in.Channel, len(in.Reason))
	return nil
}
