# 08 — Hard-delete a store when it disappears from Odoo, replacing SoftDeleteStores

**What to build:** Replace `SyncStores`' soft-delete of stores no longer reported by Odoo with a hard `DELETE FROM store`, cascading to that store's wifi whitelist and nulling any employee's `store_id` pointing at it. See [ADR-0005](/docs/adr/0005-store-removal-from-odoo-is-hard-delete.md).

**Blocked by:** 07 (removes the `is_active`/`wifi_whitelist_enabled` filter this ticket also touches; sequencing avoids editing the same query line twice)

**Status:** done

- [ ] New migration (`000NN_employees_store_id_set_null_on_delete.sql`): drop and recreate the `employees.store_id` FK constraint with `ON DELETE SET NULL` (constraint added in migration `00007_add_employees_store_id.sql` currently has no `ON DELETE` clause, defaulting to `NO ACTION`). Down migration restores the FK without `ON DELETE SET NULL`.
- [ ] `FindStoresNotInOdoo`'s query drops its `WHERE is_active = true` (or `wifi_whitelist_enabled = true`, depending on ticket 07's landing state) filter — becomes `WHERE odoo_store_id IS NOT NULL AND odoo_store_id != ALL($1::varchar[])`. Without this, a store an admin had set `wifi_whitelist_enabled = false` on would never be selected as stale and would never get cleaned up once it left Odoo.
- [ ] New `repo.Querier` query, e.g. `DeleteStores(ctx, ids []int64) (int64, error)`: `DELETE FROM store WHERE id = ANY($1::bigint[])`, returning affected row count. Replaces `SoftDeleteStores`, which is removed.
- [ ] `SyncStores` (`internal/stores/service.go`) calls the new delete query instead of `SoftDeleteStores`, still inside the same transaction as the rest of the sync. `SyncSummary.DeletedStores` continues to report the affected row count — same field, now counting hard deletes.
- [ ] No application-level `UPDATE employees SET store_id = NULL ...` needed — the FK's `ON DELETE SET NULL` handles it atomically as part of the `DELETE FROM store`.
- [ ] `store_wifi_ip`/`store_wifi_mac` need no new query — `ON DELETE CASCADE` (migration `00006_create_store_geofence.sql`) already removes their rows when the parent `store` row is deleted.
- [ ] `.scratch/store-wifi-credentials/spec.md` updated: note that store removal is a hard delete triggered by the Odoo sync (distinct from the "no whole-store delete" ruling in [ADR-0003](/docs/adr/0003-store-delete-is-per-entry-not-whole-store.md), which is about the admin-facing API, not the sync).
- [ ] Service seam (`internal/stores/service_test.go`): update `TestStoreService_SyncStores` — a store present in the mocked `FindStoresNotInOdoo` result is hard-deleted (assert the new delete query is called with the right ids), not soft-deleted; `SyncSummary.DeletedStores` still reflects the count. The employees-nulling and wifi-cascade behavior are DB-constraint-level guarantees (FK `ON DELETE SET NULL`, table `ON DELETE CASCADE`), not application logic — out of scope for the mocked-`Querier` service seam, and this repo has no DB-backed integration test infrastructure to add a seam for them; verified by the migration being declarative and by manual/staging checks.
