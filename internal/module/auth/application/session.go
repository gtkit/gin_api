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
func (s *Service) ChangePassword(ctx context.Context, request ChangePasswordRequest) error {
	if s.sessions == nil {
		return ErrLifecycleDisabled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.UserID <= 0 || request.SessionVersion < 0 || request.SessionVersion == 1<<63-1 {
		return ErrSessionExpired
	}
	if request.OldPassword == "" || len(request.OldPassword) > 72 || !utf8.ValidString(request.NewPassword) || len(request.NewPassword) > 72 || utf8.RuneCountInString(request.NewPassword) < 8 {
		return ErrInvalidPassword
	}
	if err := s.gateAccount(ctx, "user:"+strconv.FormatInt(request.UserID, 10), "password-change"); err != nil {
		return err
	}
	credential, found, err := s.sessions.FindByID(ctx, request.UserID)
	if err != nil {
		return fmt.Errorf("auth: read current credential: %w", err)
	}
	if !found || !credential.Active || credential.AuthVersion != request.SessionVersion {
		return ErrSessionExpired
	}
	if err := s.hasher.Compare(ctx, credential.PasswordHash, request.OldPassword); err != nil {
		return err
	}
	hash, err := s.hasher.Hash(ctx, request.NewPassword)
	if err != nil {
		return err
	}
	changed, err := s.sessions.ChangePassword(ctx, request.UserID, request.SessionVersion, credential.PasswordHash, hash)
	if err != nil {
		return fmt.Errorf("auth: change password: %w", err)
	}
	if !changed {
		return ErrSessionExpired
	}
	s.audit(ctx, "password changed and all sessions invalidated: uid=%d", request.UserID)
	return nil
}

// LogoutAll invalidates current and other device sessions. It does not cancel
// requests whose authentication already completed before this write.
func (s *Service) LogoutAll(ctx context.Context, request LogoutAllRequest) error {
	if s.sessions == nil {
		return ErrLifecycleDisabled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.UserID <= 0 || request.SessionVersion < 0 || request.SessionVersion == 1<<63-1 {
		return ErrSessionExpired
	}
	changed, err := s.sessions.RevokeSessions(ctx, request.UserID, request.SessionVersion)
	if err != nil {
		return fmt.Errorf("auth: revoke account sessions: %w", err)
	}
	if !changed {
		return ErrSessionExpired
	}
	s.audit(ctx, "all sessions invalidated: uid=%d", request.UserID)
	return nil
}
