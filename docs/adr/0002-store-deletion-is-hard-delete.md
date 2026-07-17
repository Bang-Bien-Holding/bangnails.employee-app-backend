---
status: superseded by ADR-0003
---

# Store deletion is a hard delete, mirroring employees

`DELETE /v1/stores/{id}` and the bulk `DELETE /v1/stores` mirror `employees`' `DeleteEmployee`/`BulkDeleteEmployees` exactly: a real `DELETE FROM store`, cascading to `store_wifi_ip`/`store_wifi_mac`. We considered soft-delete (`is_active = false`) instead, since it avoids data loss, but rejected it: `SyncStores` already unconditionally sets `is_active = true` on every store still present in Odoo each run, so a soft-deleted store would just get silently reactivated by the next sync — soft delete wouldn't actually protect anything here. Hard delete is accepted along with its own risk: if the deleted store is still present in Odoo, the next sync recreates it as a fresh row with an empty wifi whitelist (the old whitelist rows are cascade-deleted, unrecoverable). In practice an admin deletes a store because it's permanently closed, presumably also being removed from Odoo around the same time — the resurrection case is an accepted edge case, not solved here.

## Superseded

This entire premise was based on a misreading of the delete mockup: the screen shows deleting IP/MAC whitelist entries belonging to a store, not deleting the store itself. Nothing here was ever implemented. See [ADR-0003](/docs/adr/0003-store-delete-is-per-entry-not-whole-store.md) for the corrected design — there is no whole-store delete endpoint.
