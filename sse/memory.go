package sse

import "context"

// memoryDriver delivers to the connections of this instance only: no
// persistence, replay, retry queue or cross-process fanout.
type memoryDriver struct{ m *Manager }

func (d *memoryDriver) start(_ context.Context, m *Manager) error { d.m = m; return nil }

// publish dispatches locally. Accepted means the local manager ran the
// dispatch; a user with no stream here is a successful no-op.
func (d *memoryDriver) publish(_ context.Context, w wire) error {
	d.m.deliver(w)
	return nil
}

func (d *memoryDriver) sync(context.Context, string, func() bool) error { return nil }
func (d *memoryDriver) ready(context.Context) error                     { return nil }
func (d *memoryDriver) reconnects() uint64                              { return 0 }
func (d *memoryDriver) stop(context.Context) error                      { return nil }
