# 10 — PATCH /v1/stores/{id}/wifi-whitelist-enabled (single-store toggle, locked)

**What to build:** A dedicated, optimistic-locked endpoint for the list screen's per-row Activate/Deactivate toggle, replacing the field's old home on `PATCH /v1/stores/{id}`. See [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md).

**Blocked by:** 07 (removes `wifi_whitelist_enabled` from `patchStoreParams`/`UpdateStore`; this ticket adds the field's new home)

**Status:** done

- [ ] `PATCH /v1/stores/{id}/wifi-whitelist-enabled` registered in the router, distinct from `PATCH /v1/stores/{id}`.
- [ ] Request body: `{"updated_at": "...", "wifi_whitelist_enabled": true}`. Both fields required (`updated_at` a `time.Time` tagged `required`; `wifi_whitelist_enabled` a `*bool` tagged `required` — still a pointer despite always being required, since a plain `bool` can't distinguish an omitted field from an explicit `false`, which is this endpoint's other valid value).
- [ ] Optimistic-locked like the rest of the single-store surface: the submitted `updated_at` is checked against the store's current `updated_at` inside a transaction. A mismatch updates nothing and returns `409 Conflict` (`ErrStoreConflict`, reused from the existing sentinel).
- [ ] A store id that doesn't exist returns `404` (`ErrStoreNotFound`, reused).
- [ ] Every successful call bumps `store.updated_at` to `now()`.
- [ ] New `repo.Querier` query: `UPDATE store SET wifi_whitelist_enabled = $1, updated_at = now() WHERE id = $2 AND updated_at = $3 RETURNING id, updated_at` — same conditional-update-returning-0-rows-on-mismatch shape as the existing geofence query (ticket 02), run in its own transaction (no other tables touched, so no need for the wifi-list-replace transaction scaffold).
- [ ] `Service` gains a new method, e.g. `SetStoreWifiWhitelistEnabled(ctx, id int64, params SetWifiWhitelistEnabledParams) (StoreDetail, error)` (naming implementer's choice, matching this package's existing convention).
- [ ] Response body on success (`200`): `{"id": 12, "wifi_whitelist_enabled": true, "updated_at": "..."}` — deliberately not `204`, unlike `employees`' `SetEmployeeActive` precedent, since this endpoint is lock-guarded and the caller needs the fresh `updated_at` to keep toggling without a refetch.
- [ ] Service seam: tests cover a successful toggle (on and off) bumping `updated_at` and returning fresh state, `updated_at` mismatch surfacing as the conflict sentinel with zero side effects, not-found mapping.
- [ ] Handler seam: tests cover `200` success with the documented response shape, `400` for missing `updated_at`/`wifi_whitelist_enabled`, `404`, `409`.
