package dbx

import (
	"context"
	"errors"
	"testing"
)

func TestDiffReplace(t *testing.T) {
	ctx := context.Background()
	dbErr := errors.New("connection refused")

	t.Run("runs delete then insert in order", func(t *testing.T) {
		var calls []string
		deleteFn := func(context.Context) error { calls = append(calls, "delete"); return nil }
		insertFn := func(context.Context) error { calls = append(calls, "insert"); return nil }

		if err := DiffReplace(ctx, deleteFn, insertFn); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(calls) != 2 || calls[0] != "delete" || calls[1] != "insert" {
			t.Errorf("expected [delete insert], got %v", calls)
		}
	})

	t.Run("insert still runs when there's nothing to insert", func(t *testing.T) {
		insertCalled := false
		deleteFn := func(context.Context) error { return nil }
		insertFn := func(context.Context) error { insertCalled = true; return nil }

		if err := DiffReplace(ctx, deleteFn, insertFn); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !insertCalled {
			t.Error("expected insertFn to be called even with nothing to insert")
		}
	})

	t.Run("a failing delete short-circuits and skips insert", func(t *testing.T) {
		insertCalled := false
		deleteFn := func(context.Context) error { return dbErr }
		insertFn := func(context.Context) error { insertCalled = true; return nil }

		err := DiffReplace(ctx, deleteFn, insertFn)
		if !errors.Is(err, dbErr) {
			t.Errorf("expected %v, got %v", dbErr, err)
		}
		if insertCalled {
			t.Error("expected insertFn NOT to be called after delete fails")
		}
	})

	t.Run("propagates a failing insert", func(t *testing.T) {
		deleteFn := func(context.Context) error { return nil }
		insertFn := func(context.Context) error { return dbErr }

		err := DiffReplace(ctx, deleteFn, insertFn)
		if !errors.Is(err, dbErr) {
			t.Errorf("expected %v, got %v", dbErr, err)
		}
	})
}
