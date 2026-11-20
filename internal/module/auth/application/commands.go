package application

import "time"

// RegisterRequest describes the business input for account registration.
type RegisterRequest struct {
	Phone    string
	Password string
	Nickname string
}

// LoginRequest describes the business input for credential authentication.
type LoginRequest struct {
	Phone    string
	Password string
}

// RefreshRequest describes the business input for access-token refresh.
type RefreshRequest struct {
	RefreshToken string
}

// LogoutRequest describes the authenticated token pair to revoke.
type LogoutRequest struct {
	TokenID      string
	ExpiresAt    time.Time
	RefreshToken string
}

// ChangePasswordRequest describes an authenticated password change.
type ChangePasswordRequest struct {
	UserID         int64
	SessionVersion int64
	OldPassword    string
	NewPassword    string
}

// LogoutAllRequest describes account-wide session invalidation.
type LogoutAllRequest struct {
	UserID         int64
	SessionVersion int64
}
