package jwtauth

import (
	"fmt"
	"strconv"

	"github.com/gtkit/gojwt/claims"
)

// SessionVersionKey is scoped to the JWT extension object, not a top-level claim.
const SessionVersionKey = "session_version"

// SessionVersion preserves tokens issued before versioning as version zero.
// An explicitly malformed value is never treated as an absent value.
func SessionVersion(value *claims.Claims) (int64, error) {
	if value == nil {
		return 0, ErrTokenInvalid
	}
	raw, present := value.Ext[SessionVersionKey]
	if !present {
		return 0, nil
	}
	text, ok := raw.(string)
	if !ok {
		return 0, fmt.Errorf("%w: invalid session version type", ErrTokenInvalid)
	}
	version, err := strconv.ParseInt(text, 10, 64)
	if err != nil || version < 0 || strconv.FormatInt(version, 10) != text {
		return 0, fmt.Errorf("%w: invalid session version", ErrTokenInvalid)
	}
	return version, nil
}
