// Package core contains errors shared by process resource owners.
package core

import "errors"

// ErrComponentState reports an unavailable resource or invalid owner state.
var ErrComponentState = errors.New("invalid component state")
