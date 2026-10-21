// Package core contains the process skeleton shared by compile-time plugins.
package core

import "context"

// Plugin is the lifecycle contract for a compile-time registered component.
//
// Dependencies are declared at the registration site, so implementations only
// need to provide the four lifecycle methods. Stop must release resources that
// Init or Start acquired, even when the corresponding method returned an error.
// Init, Start and Stop must respect their operation context and return after
// cancellation. They run synchronously; the registry cannot forcibly interrupt
// a callback. A plugin owns any background workers and must cancel and join them
// in Stop. Init/Start contexts expire when those operations end and must not be
// used as the lifetime context of background workers. Callbacks must not reenter
// their registry, call runtime.Goexit, or terminate the process.
type Plugin interface {
	Name() string
	Init(context.Context) error
	Start(context.Context) error
	Stop(context.Context) error
}
