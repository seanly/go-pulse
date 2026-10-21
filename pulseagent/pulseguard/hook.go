package pulseguard

import (
	"sync/atomic"
)

// Live is a Guard that can be replaced when the server pushes new deny
// entries, without restarting the connection.
//
// The pointer is swapped rather than the contents mutated, so the check path
// stays lock-free: a job being admitted reads one pointer.
type Live struct {
	guard atomic.Pointer[Guard]
	base  Options
}

// NewLive compiles the local policy and returns a Live holding it.
func NewLive(o Options) (*Live, error) {
	g, err := New(o)
	if err != nil {
		return nil, err
	}
	l := &Live{base: o}
	l.guard.Store(g)
	return l, nil
}

// Guard returns the policy currently in effect. An Executor holds the Live and
// calls this per job, so a push takes effect on the next job rather than at
// the next reconnect.
func (l *Live) Guard() *Guard { return l.guard.Load() }

// ConfigPayloadHandler returns a handler for pulseagent's
// Options.OnConfigPayload.
//
// Rejecting a payload here discards the ENTIRE config push, which is the
// intended behaviour: a deny list that failed to compile must not be partially
// applied, and the agent keeps the policy it already had.
func (l *Live) ConfigPayloadHandler() func([]byte) error {
	return func(payload []byte) error {
		pushed, err := ParsePayload(payload)
		if err != nil {
			return err
		}
		g, err := Merge(l.base, pushed)
		if err != nil {
			return err
		}
		l.guard.Store(g)
		return nil
	}
}
