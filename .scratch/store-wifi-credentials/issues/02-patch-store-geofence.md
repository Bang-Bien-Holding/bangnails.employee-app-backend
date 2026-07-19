# 02 — PATCH /v1/stores/{id}: geofence update with optimistic concurrency

**What to build:** An admin can update a store's geofence (latitude, longitude, radius) via `PATCH /v1/stores/{id}`, guarded against two admins overwriting each other's concurrent edits. This ticket establishes the PATCH route, the transaction scaffold, and the optimistic-concurrency mechanism that ticket 03 reuses when it adds wifi whitelist replacement to the same endpoint.

**Blocked by:** 01 (reuses the response shape and not-found handling from the GET ticket)

**Status:** done

- [ ] `PATCH /v1/stores/{id}` is registered in the router.
- [ ] Request body accepts `updated_at` (required on every request) plus `latitude`/`longitude`/`radius_meters` as an all-or-nothing group: if any one of the three is present, all three must be present; if none are present, the geofence is left untouched.
- [ ] `latitude` is validated to -90..90, `longitude` to -180..180, `radius_meters` to the range 1–1000. A partial group (1 or 2 of 3 present) or an out-of-range value returns `400`.
- [ ] Missing `updated_at` returns `400`.
- [ ] The submitted `updated_at` is checked against the store's current `updated_at` inside the same transaction as the update. A mismatch updates nothing and returns `409 Conflict`.
- [ ] Every successful PATCH bumps `store.updated_at` to `now()`, even when only the geofence changed (this same bump-on-any-change behavior is what ticket 03 will also rely on for wifi-list-only edits).
- [ ] A store id that doesn't exist returns `404`, consistent with ticket 01. A wifi-disabled store is not treated as not-found.
- [ ] The response body on success is the same shape as `GET /v1/stores/{id}` (ticket 01), reflecting the new geofence and bumped `updated_at`.
- [ ] Service seam: tests against a mocked `repo.Querier` cover a successful geofence update, the `updated_at` mismatch → conflict sentinel error with zero side effects, and not-found mapping.
- [ ] Handler seam: tests against a mocked `Service` cover `200` success, `400` for a partial geofence group / out-of-range radius / missing `updated_at`, `404`, and `409`.
