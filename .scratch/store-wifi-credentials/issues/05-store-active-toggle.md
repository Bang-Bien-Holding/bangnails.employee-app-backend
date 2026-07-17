# 05 — PATCH /v1/stores/{id}: is_active toggle, and stop treating inactive as not-found

**What to build:** Two changes that are one unit of work, per [ADR-0001](/docs/adr/0001-store-is-active-is-not-a-tombstone.md):

1. `PATCH /v1/stores/{id}` (tickets 02/03) gains an optional `is_active` field — the list screen's Activate toggle.
2. `GetStoreByID` and `UpdateStoreGeofence` stop filtering on `is_active = true`. `404` becomes purely "no row with this id" on both `GET /v1/stores/{id}` and `PATCH /v1/stores/{id}` — required for (1), since the old filter made it impossible to ever PATCH a store back to active (the query excluded it before the update could run).

**Blocked by:** 02, 03 (extends the same PATCH handler/service method/transaction, and changes the not-found semantics both tickets shipped)

**Status:** done

- [ ] `patchStoreParams` gains `is_active *bool` (nil = omitted = untouched, same omit-vs-explicit convention as the geofence fields).
- [ ] `GetStoreByID`'s query drops `AND is_active = true` → `WHERE id = $1`.
- [ ] `UpdateStoreGeofence`'s query drops `AND is_active = true` → `WHERE id = sqlc.arg(id) AND updated_at = sqlc.arg(expected_updated_at)`.
- [ ] `UpdateStore`'s not-found-vs-conflict disambiguation (the follow-up `GetStoreByID` call after a no-rows update) still correctly returns `ErrStoreNotFound` for a genuinely missing id and `ErrStoreConflict` for a stale `updated_at`, now that `is_active` is out of the equation.
- [ ] `ErrStoreNotFound`'s doc comment no longer references `is_active`/soft-delete.
- [ ] A PATCH with `is_active: true` against a currently-inactive store succeeds (200), reactivating it — the scenario the old filter made impossible.
- [ ] A PATCH with `is_active: false` deactivates a store without touching its geofence or wifi lists when those fields are omitted.
- [ ] `GET /v1/stores/{id}` on an inactive store now returns `200` with `"is_active": false`, not `404`.
- [ ] Existing ticket 01/02 tests asserting `is_active = false` → `404` are updated to reflect the new behavior (inactive store → `200`; only a genuinely absent id → `404`).
- [ ] Service seam: tests cover reactivating an inactive store, deactivating an active one, and `is_active` omitted leaving the flag untouched.
- [ ] Handler seam: tests cover the success response reflecting the new `is_active` value, and confirm `404` now only fires for a nonexistent id.
