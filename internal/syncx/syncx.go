// Package syncx holds small concurrency helpers shared across resource
// packages.
package syncx

import "sync"

// Guard is a single-flight lock for a background job: only one run can be
// in flight at a time, and callers can poll whether one currently is.
type Guard struct {
	mu      sync.Mutex
	syncing bool
}

// TryStart claims the guard for a new run, returning false if one is
// already in flight. A successful TryStart must be paired with a later
// Finish once the run completes.
func (g *Guard) TryStart() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.syncing {
		return false
	}
	g.syncing = true
	return true
}

// Finish releases the guard, allowing a subsequent TryStart to succeed.
func (g *Guard) Finish() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.syncing = false
}

// Syncing reports whether a run is currently in flight.
func (g *Guard) Syncing() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.syncing
}
