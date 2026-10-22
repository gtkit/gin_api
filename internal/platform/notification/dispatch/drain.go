package dispatch

import "context"

// BindDrain cancels this instance's sends when the owner's cleanup is canceled.
// Releasing the binding neither stops channels nor restores a canceled lifetime.
func (p *Owner) BindDrain(ctx context.Context) func() {
	return p.channel.BindDrain(ctx)
}
