package application

import (
	"context"
	"fmt"
	"strconv"
	"unicode/utf8"
)

func (s *Service) SessionLifecycleEnabled() bool { return s != nil && s.sessions != nil }

func (s *Service) issueAccess(ctx context.Context, uid, version int64) (string, error) {
	if s.sessionAccess != nil {
		return s.sessionAccess.IssueForSession(ctx, uid, version)
	}
	return s.access.Issue(ctx, uid)
}

func (s *Service) peekRefresh(ctx context.Context, token string) (int64, int64, error) {
	if s.sessionRefresh != nil {
		return s.sessionRefresh.PeekSession(ctx, token)
	}
	uid, err := s.refresh.Peek(ctx, token)
	return uid, 0, err
}

func (s *Service) consumeRefresh(ctx context.Context, token string) (int64, int64, string, error) {
	if s.sessionRefresh != nil {
		return s.sessionRefresh.ConsumeSession(ctx, token)
	}
	uid, next, err := s.refresh.Consume(ctx, token)
	return uid, 0, next, err
}

// ChangePassword reauthenticates with the current password, then invalidates all
// sessions in the same conditional write that changes the credential hash.
func (s *Service) ChangePassword(ctx context.Context, uid, version int64, oldPassword, newPassword string) error {
	if s.sessions == nil {
		return ErrLifecycleDisabled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if uid <= 0 || version < 0 || version == 1<<63-1 {
		return ErrSessionExpired
	}
	if oldPassword == "" || len(oldPassword) > 72 || !utf8.ValidString(newPassword) || len(newPassword) > 72 || utf8.RuneCountInString(newPassword) < 8 {
		return ErrInvalidPassword
	}
	if err := s.gateAccount(ctx, "user:"+strconv.FormatInt(uid, 10), "password-change"); err != nil {
		return err
	}
	credential, found, err := s.sessions.FindByID(ctx, uid)
	if err != nil {
		return fmt.Errorf("auth: read current credential: %w", err)
	}
	if !found || !credential.Active || credential.AuthVersion != version {
		return ErrSessionExpired
	}
	if err := s.hasher.Compare(ctx, credential.PasswordHash, oldPassword); err != nil {
		return err
	}
	hash, err := s.hasher.Hash(ctx, newPassword)
	if err != nil {
		return err
	}
	changed, err := s.sessions.ChangePassword(ctx, uid, version, credential.PasswordHash, hash)
	if err != nil {
		return fmt.Errorf("auth: change password: %w", err)
	}
	if !changed {
		return ErrSessionExpired
	}
	s.audit(ctx, "password changed and all sessions invalidated: uid=%d", uid)
	return nil
}

// LogoutAll invalidates current and other device sessions. It does not cancel
// requests whose authentication already completed before this write.
func (s *Service) LogoutAll(ctx context.Context, uid, version int64) error {
	if s.sessions == nil {
		return ErrLifecycleDisabled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if uid <= 0 || version < 0 || version == 1<<63-1 {
		return ErrSessionExpired
	}
	changed, err := s.sessions.RevokeSessions(ctx, uid, version)
	if err != nil {
		return fmt.Errorf("auth: revoke account sessions: %w", err)
	}
	if !changed {
		return ErrSessionExpired
	}
	s.audit(ctx, "all sessions invalidated: uid=%d", uid)
	return nil
}
