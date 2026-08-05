// Package auth 装配本地认证模块。
//
// 本包的主要职责是**错误翻译**：把 repository、credential、tokenstore 各自的
// 错误类型翻译成 application 的哨兵错误。application 不能 import repository
// （架构边界），也不该依赖某个具体存储包的错误类型——那会让"换一个凭据存储"
// 变成一次跨层改动。翻译集中在这里，是这条边界的实际成本。
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	logger "github.com/gtkit/logger/v2"

	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/pkg/credential"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/pkg/tokenstore"
	"github.com/gtkit/gin-api/internal/repository/authcredential"
)

// credentialRepo 把仓储适配到 application 的端口。
type credentialRepo struct {
	repo *authcredential.Repository
}

func (r credentialRepo) FindByPhone(ctx context.Context, phone string) (authapp.Credential, bool, error) {
	cred, found, err := r.repo.FindByPhone(ctx, phone)
	if err != nil {
		return authapp.Credential{}, false, err
	}
	if !found {
		return authapp.Credential{}, false, nil
	}
	return authapp.Credential{
		UserID:       cred.UserID,
		PasswordHash: cred.PasswordHash,
		Active:       cred.Active,
	}, true, nil
}

func (r credentialRepo) Create(ctx context.Context, phone, passwordHash, nickname string) (int64, error) {
	uid, err := r.repo.Create(ctx, phone, passwordHash, nickname)
	if errors.Is(err, authcredential.ErrPhoneTaken) {
		return 0, authapp.ErrPhoneTaken
	}
	return uid, err
}

// passwordHasher 把密码包适配到 application 的端口。
type passwordHasher struct {
	hasher *credential.Hasher
}

func (h passwordHasher) Hash(password string) (string, error) {
	hashed, err := h.hasher.Hash(password)
	if errors.Is(err, credential.ErrPasswordTooLong) {
		return "", authapp.ErrPasswordTooLong
	}
	return hashed, err
}

func (h passwordHasher) Compare(hashed, password string) error {
	err := h.hasher.Compare(hashed, password)
	if errors.Is(err, credential.ErrMismatch) {
		return authapp.ErrInvalidCredentials
	}
	return err
}

func (h passwordHasher) CompareWithDummy(password string) error {
	err := h.hasher.CompareWithDummy(password)
	if !errors.Is(err, credential.ErrMismatch) {
		// 契约要求它恒为不匹配。真冒出别的错误说明底层实现变了——
		// 如实返回而不是掩盖：这条路径上的静默会让防账号枚举的时间掩护
		// 悄悄失效，而没有任何迹象。
		return err
	}
	return authapp.ErrInvalidCredentials
}

// refreshStore 把令牌存储适配到 application 的端口。
type refreshStore struct {
	store *tokenstore.RefreshStore
}

func (s refreshStore) Issue(ctx context.Context, uid int64) (string, error) {
	return s.store.Issue(ctx, uid)
}

func (s refreshStore) Consume(ctx context.Context, token string) (int64, string, error) {
	uid, next, err := s.store.Consume(ctx, token)
	switch {
	case err == nil:
		return uid, next, nil
	case errors.Is(err, tokenstore.ErrRefreshReused):
		// 重用单独翻译：上层据此把它记成告警级安全事件，而不是日常的
		// "令牌过期"。两者对客户端同义，对运维完全不同。
		return 0, "", authapp.ErrRefreshReused
	case errors.Is(err, tokenstore.ErrRefreshNotFound),
		errors.Is(err, tokenstore.ErrFamilyRevoked),
		errors.Is(err, tokenstore.ErrMalformedToken):
		return 0, "", authapp.ErrRefreshRejected
	default:
		// 基础设施故障保持原样：它不该被当成"令牌无效"，否则一次 Redis
		// 抖动会把所有在线用户都告知"请重新登录"。
		return 0, "", err
	}
}

func (s refreshStore) RevokeFamilyOfToken(ctx context.Context, token string) error {
	err := s.store.RevokeFamilyOfToken(ctx, token)
	if errors.Is(err, tokenstore.ErrMalformedToken) {
		// 客户端给了一个畸形的刷新令牌。它本来就不对应任何令牌族，
		// 登出的访问令牌部分已经生效，这里当作完成而不是报错。
		return nil
	}
	return err
}

// revocationList 把撤销表适配到 application 的端口。
type revocationList struct {
	list *tokenstore.RevocationList
}

func (l revocationList) Revoke(ctx context.Context, tokenID string, expiresAt time.Time) error {
	return l.list.Revoke(ctx, tokenID, expiresAt)
}

// authAuditor 把认证事件写进日志栈。
type authAuditor struct{}

func (authAuditor) Auditf(format string, args ...any) { logger.Infof(format, args...) }

// Failf 走 Warn：登录失败、重用检测、撤销失败都是安全排查的起点，
// 与"某某登录成功"共用一个级别，告警规则就只能靠解析文本。
func (authAuditor) Failf(format string, args ...any) { logger.Warnf(format, args...) }

// eventRecorder 把认证事件转发给指标仪表。
type eventRecorder struct {
	recorder *metrics.Recorder
}

func (e eventRecorder) RecordAuthEvent(ctx context.Context, event string) {
	// application 用字符串常量表达事件，指标包用具名类型：两处都要求取值
	// 有限，而类型转换在这里是安全的——application 的常量集合与
	// metrics.AuthEvent 的取值一一对应，且都由本仓维护。
	e.recorder.RecordAuthEvent(ctx, metrics.AuthEvent(event))
}

// assertPorts 是编译期检查：适配器必须满足 application 的端口。
//
// 显式断言而不是等装配时才发现：一个端口签名变更若只在 NewService 的调用处
// 报错，错误信息会指向装配函数而不是真正需要修改的适配器。
var (
	_ authapp.CredentialRepository = credentialRepo{}
	_ authapp.PasswordHasher       = passwordHasher{}
	_ authapp.RefreshStore         = refreshStore{}
	_ authapp.RevocationList       = revocationList{}
	_ authapp.Auditor              = authAuditor{}
	_ authapp.EventRecorder        = eventRecorder{}
)

// errMissingRedis 是缺少 Redis 时的装配错误。
//
// 配置校验已经在启动更早的阶段拦下这种组合，这里是第二道：装配代码不该
// 依赖"别处一定校验过"——那个前提一旦被改动打破，这里会静默地构造出一个
// 无法撤销令牌的认证服务。
var errMissingRedis = fmt.Errorf("auth: local authentication requires redis for token revocation")
