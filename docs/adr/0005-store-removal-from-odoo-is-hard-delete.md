---
status: accepted
---

# Store removal from Odoo is a hard delete, not soft-delete

Today, `SyncStores` (`internal/stores`) treats a store that's disappeared from Odoo (`FindStoresNotInOdoo`) as a soft-delete: `SoftDeleteStores` sets `is_active = false` on it. [ADR-0001](/docs/adr/0001-store-is-active-is-not-a-tombstone.md) removed the *404-on-inactive* behavior this implied, but never touched this write path — it's unchanged, still running on every sync.

[ADR-0004](/docs/adr/0004-store-wifi-whitelist-enabled-replaces-is-active.md) renames that column to `wifi_whitelist_enabled` and narrows it to a purely admin-controlled wifi-check toggle. Leaving `SoftDeleteStores` as-is would keep the exact problem ADR-0004 fixed on the other side: the sync could silently flip an admin-untouched store's wifi-check flag off, for a reason (Odoo removal) that has nothing to do with wifi.

## Decision

Replace `SoftDeleteStores` entirely with a hard `DELETE FROM store WHERE id = ANY(staleStoreIDs)`, run in the same transaction as the rest of `SyncStores`.

- `store_wifi_ip` / `store_wifi_mac` already cascade-delete (`ON DELETE CASCADE`, migration `00006_create_store_geofence.sql`) — no new query needed for those tables.
- `employees.store_id` (migration `00007_add_employees_store_id.sql`) currently has no `ON DELETE` clause (Postgres default `NO ACTION`), which would make the store delete fail outright if any employee still references it. A new migration changes that FK to `ON DELETE SET NULL`, so an affected employee's `store_id` is nulled automatically and atomically as part of the same delete — no application-level `UPDATE` needed, and no future store-deleting code path can forget to do it.
- `FindStoresNotInOdoo`'s query drops its `WHERE is_active = true` filter (soon `wifi_whitelist_enabled = true`). That filter existed to avoid reprocessing an already-soft-deleted store every run; it no longer makes sense once the column is unrelated to existence and removal is a hard delete rather than a repeatable flag-set. Left in place, it would have introduced a real bug: a store an admin had disabled wifi on (`wifi_whitelist_enabled = false`) would never be selected by this query, and so would never be caught as stale and deleted — permanently orphaned once it left Odoo.

### Accepted risk

This delete is fully automatic — triggered by a sync run's Odoo response omitting a store's id, with no admin confirmation. A transient Odoo issue (a partial page, a flaky call, a pagination bug) that momentarily under-reports the store list would cause a real, unrecoverable delete: cascaded whitelist entries and nulled employee links can't be undone by Odoo reporting the store again next run. This is a strictly worse version of the risk [ADR-0002](/docs/adr/0002-store-deletion-is-hard-delete.md) already accepted for a soft-delete (which was at least resurrection-safe). No safeguard (consecutive-miss threshold, time-based grace window, sync-size circuit breaker, or a two-step flag-then-confirm flow) is being added for now — accepted consistent with this codebase's existing precedent of not engineering around sync edge cases that may never occur in practice. Revisit if this ever actually happens in production.

## Consequences

- `SoftDeleteStores` query and its call site in `SyncStores` are removed.
- New migration: `employees.store_id` FK becomes `ON DELETE SET NULL`.
- `FindStoresNotInOdoo`'s query loses its `is_active`/`wifi_whitelist_enabled` filter.
- `SyncSummary.DeletedStores` now counts hard deletes instead of soft-deletes — same field, different underlying operation.
- Existing `SyncStores` service tests asserting soft-delete behavior are updated to assert the hard delete instead.
- See ticket [08](/.scratch/store-wifi-credentials/issues/08-hard-delete-store-on-odoo-removal.md) for the implementation checklist.
