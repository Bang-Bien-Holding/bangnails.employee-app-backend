---
status: superseded by ADR-0004
---

# store.is_active is a normal editable field, not a soft-delete tombstone

Tickets 01/02 originally treated `store.is_active = false` as a not-found condition (`GetStoreByID` and `UpdateStoreGeofence` both filtered `WHERE is_active = true`), reusing it as a soft-delete flag for stores removed from Odoo. Adding an admin-facing "Activate" toggle (the store list screen) needs to fetch, display, and re-enable a deactivated store — which the tombstone semantics made structurally impossible: the query meant to reactivate a store excluded it before the update could run. We're dropping `is_active` from both WHERE clauses; `404` now means purely "no row with this id," and the toggle lives on the existing `PATCH /v1/stores/{id}` (an added optional `is_active` field) rather than a new sub-resource endpoint, since that PATCH is already partial-update-shaped. This matches how `employees` already treats `is_active` — `GetEmployeeByID` has no such filter.

## Consequences

- Touches already-shipped ticket 01/02 code: the `GetStoreByID`/`UpdateStoreGeofence` queries, `ErrStoreNotFound`'s doc comment, and their existing tests that assumed 404-for-inactive.
- `UpdateStore`'s not-found-vs-conflict disambiguation (re-running `GetStoreByID` after a no-rows update) still works with the narrower WHERE clause — it now only distinguishes "id doesn't exist" from "updated_at stale," which was its original intent anyway.

## Superseded

This ADR's core ruling — the column is a normal editable field, not a soft-delete tombstone, with no WHERE-clause filtering on `GET`/`PATCH` — still stands. What's superseded is the column's name and scope: `is_active` implied a general store-operational-status concept, but its only real effect was ever gating the wifi-whitelist login check specifically. It's renamed to `wifi_whitelist_enabled` and its documented effect narrowed accordingly — see [ADR-0004](/docs/adr/0004-store-wifi-whitelist-enabled-replaces-is-active.md). Separately, the "reused as a soft-delete flag for stores removed from Odoo" mechanism this ADR's opening paragraph refers to (`SoftDeleteStores`, still live in `SyncStores` at the time this ADR was written) is itself replaced by a hard delete — see [ADR-0005](/docs/adr/0005-store-removal-from-odoo-is-hard-delete.md).

This ADR's other specific call — "the toggle lives on the existing `PATCH /v1/stores/{id}`... rather than a new sub-resource endpoint" — is itself superseded: the field is later moved to its own dedicated, locked endpoint. See [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md).
