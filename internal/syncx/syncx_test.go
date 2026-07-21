package syncx

import (
	"sync"
	"testing"
)

func TestGuard(t *testing.T) {
	t.Run("TryStart succeeds when idle", func(t *testing.T) {
		var g Guard
		if !g.TryStart() {
			t.Fatal("expected TryStart to succeed on an idle guard")
		}
	})

	t.Run("TryStart fails while a run is in flight", func(t *testing.T) {
		var g Guard
		if !g.TryStart() {
			t.Fatal("expected first TryStart to succeed")
		}
		if g.TryStart() {
			t.Error("expected second TryStart to fail while a run is in flight")
		}
	})

	t.Run("TryStart succeeds again after Finish", func(t *testing.T) {
		var g Guard
		g.TryStart()
		g.Finish()
		if !g.TryStart() {
			t.Error("expected TryStart to succeed after Finish released the guard")
		}
	})

	t.Run("Syncing reflects the current state", func(t *testing.T) {
		var g Guard
		if g.Syncing() {
			t.Error("expected Syncing() = false before any TryStart")
		}
		g.TryStart()
		if !g.Syncing() {
			t.Error("expected Syncing() = true after TryStart")
		}
		g.Finish()
		if g.Syncing() {
			t.Error("expected Syncing() = false after Finish")
		}
	})

	t.Run("only one concurrent TryStart wins", func(t *testing.T) {
		var g Guard
		const attempts = 100
		var wg sync.WaitGroup
		var wins int32
		var mu sync.Mutex

		wg.Add(attempts)
		for range attempts {
			go func() {
				defer wg.Done()
				if g.TryStart() {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		if wins != 1 {
			t.Errorf("expected exactly 1 winning TryStart, got %d", wins)
		}
	})
}
