---
status: accepted
---

# Store "delete" is per-entry IP/MAC whitelist removal, not whole-store deletion

[ADR-0002](/docs/adr/0002-store-deletion-is-hard-delete.md) specced `DELETE /v1/stores/{id}` and a bulk `DELETE /v1/stores` as a hard delete of the store row, cascading to its wifi whitelist. That was based on a misreading of the delete mockup: the screen shows removing specific IP/MAC entries from one store's whitelist (a trash action per entry, or multi-select across entries within a store), not removing stores from the list. There is no whole-store delete in this feature — a store's lifecycle (creation, renaming, permanent closure) stays entirely owned by the one-way Odoo sync (`SyncStores`); `is_active` (see [ADR-0001](/docs/adr/0001-store-is-active-is-not-a-tombstone.md)) is still the only per-store state an admin can toggle directly.

The corrected feature is `DELETE /v1/stores/{id}/wifi-whitelist`, scoped to one store, removing submitted IP and/or MAC values from that store's whitelist:

- Deletes **by value**, not by the tables' internal `id` columns — `store_wifi_ip`/`store_wifi_mac` already enforce `UNIQUE (store_id, ip_address)` / `UNIQUE (store_id, mac_address)`, so a value unambiguously identifies the row to remove within a store. This avoids adding per-entry ids to the `GET`/`PATCH` response shape, which stays plain string arrays.
- Is additive to `PATCH /v1/stores/{id}`'s existing full-replace semantics for `ip_addresses`/`mac_addresses`, not a replacement for them — PATCH remains the bulk add/edit path; this endpoint is the surgical single/multi-entry removal path referenced directly from the list screen.
- Reuses PATCH's `updated_at` optimistic-lock convention: the same aggregate-wide concurrency protection applies since this also mutates the whitelist tables guarded by that lock. `updated_at` only bumps when at least one entry is actually deleted.
- Is bulk-capable and best-effort per entry, matching the `employees` bulk-delete pattern in spirit, but keyed by `{value, type}` instead of an integer id, since there's no single integer identifying "an IP or a MAC."

See ticket [06](/.scratch/store-wifi-credentials/issues/06-delete-wifi-whitelist-entries.md) for the implementation checklist.
