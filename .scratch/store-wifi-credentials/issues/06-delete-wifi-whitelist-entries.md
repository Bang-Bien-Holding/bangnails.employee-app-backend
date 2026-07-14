# 06 — DELETE /v1/stores/{id}/wifi-whitelist (per-entry IP/MAC delete)

**What to build:** A bulk-capable endpoint that removes specific IP and/or MAC entries from one store's whitelist, identified by value (not internal id) — the list screen's per-entry delete action. See [ADR-0003](/docs/adr/0003-store-delete-is-per-entry-not-whole-store.md) for why this replaced the originally-specced whole-store delete (superseding [ADR-0002](/docs/adr/0002-store-deletion-is-hard-delete.md)).

**Blocked by:** 03 (reuses `store_wifi_ip`/`store_wifi_mac` validation and the `withTx` transaction pattern), 02 (reuses the `updated_at` optimistic-lock pattern)

**Status:** done

- [ ] `DELETE /v1/stores/{id}/wifi-whitelist` is registered in the router, scoped to one store.
- [ ] Request body: `{"updated_at": "...", "ip_addresses": ["138.101.10.1"], "mac_addresses": ["AA:BB:CC:DD:EE:FF"]}`. Both arrays optional and independent, but at least one value must be present across the two — `validate:"required_without_all"`-style, reject an empty/all-omitted request with `400`.
- [ ] Each array element validated with the same format rules as PATCH (IPv4 only for `ip_addresses`, MAC-48 for `mac_addresses`) and the same in-array `unique` duplicate rejection (`400`).
- [ ] `updated_at` is **required** and checked the same way as PATCH: must match the store's current `updated_at` at the moment the transaction runs, or the whole request is rejected with `409` and nothing is deleted.
- [ ] Deletes are by **value**, not internal DB id — `store_wifi_ip`/`store_wifi_mac` already enforce `UNIQUE (store_id, ip_address)` / `UNIQUE (store_id, mac_address)`, so no response-shape change is needed on `GET`/`PATCH` to expose per-entry ids.
- [ ] This is additive to PATCH's existing full-replace semantics for `ip_addresses`/`mac_addresses` — PATCH is unchanged and still supports bulk add/replace; this endpoint is the surgical single/multi-entry removal path.
- [ ] Per-entry, best-effort semantics: a submitted value not currently in the store's whitelist is reported as a failed entry but doesn't block or roll back the others (same spirit as `BulkDeleteEmployees`, but keyed by value+type instead of id).
- [ ] Response is a JSON array of a new result shape covering both IP and MAC results together: `{"value": "138.101.10.1", "type": "ip", "success": true}` / `{"value": "...", "type": "mac", "success": false, "error": "..."}`.
- [ ] `store.updated_at` is bumped to `now()` **only if at least one entry was actually deleted** — a request where every entry fails (e.g. all values are typos) leaves `updated_at` untouched.
- [ ] `Service` gains `DeleteWifiWhitelistEntries(ctx, storeID int64, params DeleteWifiWhitelistParams) ([]WifiWhitelistDeleteResult, error)` (naming implementer's choice), matching the transactional pattern `UpdateStore` already uses.
- [ ] New `repo.Querier` queries: delete-by-value for `store_wifi_ip` and `store_wifi_mac` (returning affected rows so the service can report per-value success/failure), plus the conditional `updated_at` bump reusing `UpdateStoreGeofence`'s optimistic-lock query shape (or a dedicated query if the geofence fields shouldn't be touched).
- [ ] Service seam: tests cover deleting a mix of present/absent IP and MAC values in one call, `updated_at` mismatch surfacing as the conflict sentinel with zero side effects, `updated_at` left untouched when every entry fails, and not-found mapping for an unknown store id.
- [ ] Handler seam: tests cover `400` for empty request / bad format / duplicate values, `404` for unknown store id, `409` for `updated_at` mismatch, and the success response's per-entry result array shape.
