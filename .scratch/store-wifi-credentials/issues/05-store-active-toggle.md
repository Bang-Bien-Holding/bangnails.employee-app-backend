# 05 — PATCH /v1/stores/{id}: wifi_whitelist_enabled toggle, and stop treating inactive as not-found

**What to build:** Two changes that are one unit of work, per [ADR-0001](/docs/adr/0001-store-is-active-is-not-a-tombstone.md):

1. `wifi_whitelist_enabled` gains its own dedicated toggle endpoint (see ticket 10, `PATCH /v1/stores/{id}/wifi-whitelist-enabled`) rather than living on `PATCH /v1/stores/{id}` — the list screen's Activate toggle drives that endpoint.
2. `GetStoreByID` and `UpdateStoreGeofence` stop filtering on `wifi_whitelist_enabled = true`. `404` becomes purely "no row with this id" on `GET /v1/stores/{id}`, `PATCH /v1/stores/{id}`, and the wifi-whitelist-enabled toggle endpoint — required for (1), since the old filter made it impossible to ever toggle a store back to enabled (the query excluded it before the update could run).

**Blocked by:** 02, 03 (extends the same PATCH handler/service method/transaction, and changes the not-found semantics both tickets shipped)

**Status:** done

- [ ] `GetStoreByID`'s query drops `AND wifi_whitelist_enabled = true` → `WHERE id = $1`.
- [ ] `UpdateStoreGeofence`'s query drops `AND wifi_whitelist_enabled = true` → `WHERE id = sqlc.arg(id) AND updated_at = sqlc.arg(expected_updated_at)`.
- [ ] `UpdateStore`'s not-found-vs-conflict disambiguation (the follow-up `GetStoreByID` call after a no-rows update) still correctly returns `ErrStoreNotFound` for a genuinely missing id and `ErrStoreConflict` for a stale `updated_at`, now that `wifi_whitelist_enabled` is out of the equation.
- [ ] `ErrStoreNotFound`'s doc comment no longer references `is_active`/soft-delete.
- [ ] `GET /v1/stores/{id}` on a wifi-disabled store now returns `200` with `"wifi_whitelist_enabled": false`, not `404`.
- [ ] Existing ticket 01/02 tests asserting `is_active = false` → `404` are updated to reflect the new behavior (wifi-disabled store → `200`; only a genuinely absent id → `404`).
- [ ] Service/handler seams for `GetStoreByID`/`UpdateStore` no longer surface `is_active`/`wifi_whitelist_enabled` as a settable field on this endpoint — that's ticket 07/10's scope; here it's `404`-vs-`200` semantics only.
