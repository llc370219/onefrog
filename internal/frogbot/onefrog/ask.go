package onefrog

import (
	"context"
	"encoding/json"
)

// Asker lets a host tool ask the user for one decision while it runs, using
// the same approval card, timeout and records as the guard. The host decides
// whether a call needs a human (its own rules and approval mode); the kernel
// only provides the card. A decision covers this one call only.
type Asker func(tool string, args json.RawMessage, risk Risk, reason string) (approved bool, note string)

type askerKey struct{}

// AskerFrom returns the asker for the tool call running in ctx, or nil.
func AskerFrom(ctx context.Context) Asker {
	a, _ := ctx.Value(askerKey{}).(Asker)
	return a
}

// TaskIDFrom returns the id of the task whose tool call is running in ctx.
func TaskIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(taskIDKey{}).(string)
	return id
}

type taskIDKey struct{}

func (g *Guard) asker(ctx context.Context, t Task, emit func(EventType, any)) Asker {
	return func(tool string, args json.RawMessage, risk Risk, reason string) (bool, string) {
		v := g.askUser(ctx, t, tool, args, ArgsHash(args), risk, reason, true, emit)
		return v.Decision == Allow, v.Reason
	}
}
