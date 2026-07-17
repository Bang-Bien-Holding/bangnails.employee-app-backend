---
status: superseded by ADR-0006
---

# store.wifi_whitelist_enabled replaces is_active; sync no longer overwrites it

`is_active` (see [ADR-0001](/docs/adr/0001-store-is-active-is-not-a-tombstone.md)) has one real effect in this codebase: it's the "Activate" toggle that enables or disables a store's login checks. Designing a store-wide "deactivate wifi" admin action surfaced two problems with keeping that name and behavior as-is.

First, the name implies a general store-operational-status concept, but the actual effect being designed is narrower: turning it off disables only the wifi-whitelist check for that store. The GPS/geofence fallback (see `CONTEXT.md`) is unaffected — an employee at a wifi-deactivated store can still log in via geofence. A name like `is_active` gives no hint of that scope; a reader would reasonably assume it gates login entirely.

Second, `UpsertStores` (the Odoo sync, `internal/stores`) unconditionally sets `is_active = true` in its `ON CONFLICT (odoo_store_id) DO UPDATE` clause for every store still present in Odoo, on every sync run. That behavior was fine when `is_active` only meant "not soft-deleted" (a sync-owned concept), but once the same column is also an admin-owned wifi-check toggle, it silently undoes an admin's deactivation the next time the sync runs.

## Decision

Rename `store.is_active` to `store.wifi_whitelist_enabled` in place — same column, same table, same boolean, same `NOT NULL DEFAULT true`. ADR-0001's ruling continues to hold under the new name: it's a normal editable field, not a soft-delete tombstone, with no `WHERE` filtering on `GET`/`PATCH`.

Narrow the documented effect: `wifi_whitelist_enabled = false` disables only the wifi-whitelist check for that store. It has no effect on the GPS/geofence fallback, and no effect on any other store field.

Drop `wifi_whitelist_enabled = true` from `UpsertStores`' `ON CONFLICT DO UPDATE` clause. The sync no longer touches this column in either direction (see [ADR-0005](/docs/adr/0005-store-removal-from-odoo-is-hard-delete.md) for the other direction — a store disappearing from Odoo). It becomes purely admin-controlled; a new store still defaults to `true` via the column default on first insert.

Two ways for an admin to set it:

- **Single store**: the existing `PATCH /v1/stores/{id}` — unchanged shape, field renamed, still under the `updated_at` optimistic-lock protection that endpoint already has for its other fields.
- **Bulk**: a new `PATCH /v1/stores` (collection-level, no id in the path), body `{"ids": [...], "wifi_whitelist_enabled": false}`, applied independently per store, best-effort, returning a `BulkActionResult[]` — identical shape to `employees`' `BulkDeleteEmployees`. This endpoint intentionally carries no optimistic lock: unlike the single-store PATCH (which edits several fields at once and could silently clobber a concurrent edit to a *different* field), this endpoint only ever writes one boolean, so there's no other field's state it could clobber — two concurrent callers converge on the same correct end state regardless of ordering.

## Consequences

- Migration renaming the column (and its `NOT NULL DEFAULT true` clause).
- Touches `UpsertStores`, `GetStoreByID`, `ListStores`, `UpdateStore`'s params/query, the PATCH/GET request and response body field name, and every existing test asserting `is_active`.
- New `Service`/`Handler`/`repo.Querier` surface for the bulk endpoint.
- `CONTEXT.md`'s Store glossary entry updated to describe the renamed, narrower-scoped field.
- See ticket [07](/.scratch/store-wifi-credentials/issues/07-rename-is-active-to-wifi-whitelist-enabled.md) for the rename and sync-behavior change, and ticket [09](/.scratch/store-wifi-credentials/issues/09-bulk-wifi-whitelist-toggle.md) for the bulk endpoint.

## Superseded

The rename, the narrowed "wifi-whitelist check only, not GPS/geofence" scope, and "purely admin-controlled, sync never writes to it" all still stand. What's superseded is the *mechanism* for the "two ways to set it" section above: the single-store path moves off `PATCH /v1/stores/{id}` onto its own dedicated, locked endpoint, and the bulk path drops its lock-exempt, best-effort-per-id design in favor of an atomic, per-id-locked one. See [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md).
