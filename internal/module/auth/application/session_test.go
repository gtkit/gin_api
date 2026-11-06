package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type sessionCredentials struct {
	*fakeCreds
	writeErr error
}

func (r *sessionCredentials) FindByID(ctx context.Context, uid int64) (Credential, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Credential{}, false, err
	}
	for _, cred := range r.byPhone {
		if cred.UserID == uid {
			return cred, true, r.findErr
		}
	}
	return Credential{}, false, r.findErr
}

func (r *sessionCredentials) ChangePassword(ctx context.Context, uid, version int64, old, next string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if r.writeErr != nil {
		return false, r.writeErr
	}
	for phone, cred := range r.byPhone {
		if cred.UserID == uid && cred.Active && cred.AuthVersion == version && (old == "" || cred.PasswordHash == old) {
			cred.AuthVersion++
			if next != "" {
				cred.PasswordHash = next
			}
			r.byPhone[phone] = cred
			return true, nil
		}
	}
	return false, nil
}

func (r *sessionCredentials) RevokeSessions(ctx context.Context, uid, version int64) (bool, error) {
	return r.ChangePassword(ctx, uid, version, "", "")
}

type sessionIssuer struct {
	fakeIssuer
	versions []int64
}

func (i *sessionIssuer) IssueForSession(ctx context.Context, uid, version int64, roles ...string) (string, error) {
	i.versions = append(i.versions, version)
	return i.Issue(ctx, uid, roles...)
}

type sessionRefresh struct {
	fakeRefresh
	version       int64
	issuedVersion int64
}

func (r *sessionRefresh) IssueForSession(ctx context.Context, uid, version int64) (string, error) {
	r.issuedVersion = version
	return r.Issue(ctx, uid)
}

func (r *sessionRefresh) PeekSession(ctx context.Context, token string) (int64, int64, error) {
	uid, err := r.Peek(ctx, token)
	return uid, r.version, err
}

func (r *sessionRefresh) ConsumeSession(ctx context.Context, token string) (int64, int64, string, error) {
	uid, next, err := r.Consume(ctx, token)
	return uid, r.version, next, err
}

func newSessionService(t *testing.T) (*Service, *sessionCredentials, *sessionIssuer, *sessionRefresh) {
	t.Helper()
	creds := &sessionCredentials{fakeCreds: newFakeCreds()}
	creds.byPhone["account"] = Credential{UserID: 7, Active: true, PasswordHash: "hashed:old-password"}
	issuer := &sessionIssuer{}
	refresh := &sessionRefresh{consumeUID: 7}
	svc, err := NewService(Options{
		Credentials: creds, Sessions: creds, Hasher: &fakeHasher{},
		AccessIssuer: issuer, RefreshStore: refresh, RevocationList: newFakeRevocation(), AccessTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, creds, issuer, refresh
}

func TestPasswordChangeAndLogoutAll(t *testing.T) {
	svc, creds, issuer, refresh := newSessionService(t)
	ctx := t.Context()
	if err := svc.ChangePassword(ctx, 7, 0, "wrong-password", "new-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := svc.ChangePassword(ctx, 7, 0, "old-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	cred, found, err := creds.FindByID(ctx, 7)
	if err != nil || !found {
		t.Fatalf("read changed credential: found=%v err=%v", found, err)
	}
	if cred.AuthVersion != 1 || cred.PasswordHash != "hashed:new-password" {
		t.Fatalf("credential change was incomplete: %+v", cred)
	}
	if _, err := svc.Refresh(ctx, "old-refresh"); !errors.Is(err, ErrRefreshRejected) || issuer.issued != 0 || refresh.consumeCalls != 0 {
		t.Fatalf("revoked refresh reached issuance/consumption: %v", err)
	}
	if err := svc.LogoutAll(ctx, 7, 0); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("stale session changed current version: %v", err)
	}
	if _, err := svc.Login(ctx, "account", "old-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password still accepted: %v", err)
	}
	if _, err := svc.Login(ctx, "account", "new-password"); err != nil {
		t.Fatal(err)
	}
	if issuer.versions[0] != 1 || refresh.issuedVersion != 1 {
		t.Fatal("login did not issue the verified credential version")
	}
	refresh.version = 1
	if err := svc.LogoutAll(ctx, 7, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(ctx, "new-refresh"); !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("logout-all did not invalidate refresh: %v", err)
	}
}

type transitionHasher struct {
	PasswordHasher
	afterCompare func()
	beforeHash   func()
}

func (h transitionHasher) Compare(ctx context.Context, hash, password string) error {
	if err := h.PasswordHasher.Compare(ctx, hash, password); err != nil {
		return err
	}
	if h.afterCompare != nil {
		h.afterCompare()
	}
	return nil
}

func (h transitionHasher) Hash(ctx context.Context, password string) (string, error) {
	if h.beforeHash != nil {
		h.beforeHash()
	}
	return h.PasswordHasher.Hash(ctx, password)
}

func TestLoginDoesNotUpgradeVersionAfterPasswordCheck(t *testing.T) {
	svc, creds, issuer, refresh := newSessionService(t)
	svc.hasher = transitionHasher{PasswordHasher: svc.hasher, afterCompare: func() {
		if ok, err := creds.RevokeSessions(t.Context(), 7, 0); !ok || err != nil {
			t.Fatalf("interleaved revocation: %v", err)
		}
	}}
	if _, err := svc.Login(t.Context(), "account", "old-password"); err != nil {
		t.Fatal(err)
	}
	if issuer.versions[0] != 0 || refresh.issuedVersion != 0 {
		t.Fatal("in-flight login upgraded a revoked credential version")
	}
	if _, err := svc.Refresh(t.Context(), "refresh"); !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("in-flight login issued a usable refresh: %v", err)
	}
}

func TestPasswordChangeRejectsInterleavedRevocation(t *testing.T) {
	svc, creds, _, _ := newSessionService(t)
	svc.hasher = transitionHasher{PasswordHasher: svc.hasher, beforeHash: func() {
		if ok, err := creds.RevokeSessions(t.Context(), 7, 0); !ok || err != nil {
			t.Fatalf("interleaved revocation: %v", err)
		}
	}}
	if err := svc.ChangePassword(t.Context(), 7, 0, "old-password", "new-password"); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("stale password update accepted: %v", err)
	}
	cred, found, err := creds.FindByID(t.Context(), 7)
	if err != nil || !found {
		t.Fatalf("read current credential: found=%v err=%v", found, err)
	}
	if cred.PasswordHash != "hashed:old-password" || cred.AuthVersion != 1 {
		t.Fatal("conflicting password update changed credentials")
	}
}

func TestSessionLifecycleFailures(t *testing.T) {
	svc, creds, _, _ := newSessionService(t)
	for _, password := range []string{"short", strings.Repeat("界", 25), string([]byte{0xff, 0xfe})} {
		if err := svc.ChangePassword(t.Context(), 7, 0, "old-password", password); !errors.Is(err, ErrInvalidPassword) {
			t.Fatalf("invalid password accepted: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := svc.LogoutAll(ctx, 7, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	failure := errors.New("database unavailable")
	creds.writeErr = failure
	if err := svc.ChangePassword(t.Context(), 7, 0, "old-password", "new-password"); !errors.Is(err, failure) {
		t.Fatalf("write failure lost: %v", err)
	}
	if err := svc.LogoutAll(t.Context(), 7, 0); !errors.Is(err, failure) {
		t.Fatalf("revoke failure lost: %v", err)
	}
	if err := newHarness(t).svc.LogoutAll(t.Context(), 7, 0); !errors.Is(err, ErrLifecycleDisabled) {
		t.Fatalf("disabled service accepted lifecycle operation: %v", err)
	}
}
