// Package dbx holds small database-layer helpers shared across resource
// packages.
package dbx

import "context"

// DiffReplace runs deleteFn then insertFn — the "delete what's no longer
// submitted, insert what's newly submitted" shape of a whole-set replace
// (see CONTEXT.md's Wifi Whitelist and Position entries). insertFn always
// runs, even when there's nothing to insert: the sqlc-generated inserts this
// is paired with are all `INSERT ... SELECT unnest(...) ON CONFLICT DO
// NOTHING`, which is already a safe no-op over an empty desired set.
func DiffReplace(ctx context.Context, deleteFn, insertFn func(context.Context) error) error {
	if err := deleteFn(ctx); err != nil {
		return err
	}
	return insertFn(ctx)
}
