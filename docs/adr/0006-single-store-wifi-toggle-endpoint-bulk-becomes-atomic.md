---
status: accepted
---

# wifi_whitelist_enabled gets its own single-store endpoint; bulk toggle becomes atomic and locked

ADR-0001 folded the "Activate" toggle into `PATCH /v1/stores/{id}` on purpose, explicitly rejecting a new sub-resource endpoint, because that PATCH was already partial-update-shaped. ADR-0004 then added a second way to set the same field — bulk `PATCH /v1/stores` — deliberately exempt from the `updated_at` optimistic lock, reasoning that a single-boolean write always converges to the same end state regardless of call order.

Revisiting both calls together surfaced two problems:

1. **The list screen's per-row toggle only ever needs to write one boolean, but is forced through the full, multi-field, lock-guarded `PATCH /v1/stores/{id}` to do it.** That endpoint requires `updated_at` unconditionally (it also guards the IP/MAC lists and geofence), so a UI action that is conceptually "click Activate" has to carry the same request weight as a full store edit.
2. **The "convergence regardless of order" argument for the lock-exempt bulk endpoint only holds when every concurrent caller wants the same value.** It breaks down the moment two admins send *different* values for the same store — e.g. deliberately intending "first request wins," a request-level `409` reject beats a silent, undetectable last-write-wins.

## Decision

**`wifi_whitelist_enabled` is removed from `PATCH /v1/stores/{id}`'s writable fields entirely.** That endpoint's response still *reports* the store's current `wifi_whitelist_enabled` (its response body mirrors `GET`), it just can no longer *set* it. This reverses ADR-0001's specific "no new sub-resource endpoint" call — the reasoning that justified it (the field fit naturally as one more omittable field on an already-partial-update PATCH) no longer applies once the field has its own endpoint with its own, lighter-weight lock semantics.

**New endpoint: `PATCH /v1/stores/{id}/wifi-whitelist-enabled`** — the list screen's per-row Activate/Deactivate action.

- Request: `{"updated_at": "...", "wifi_whitelist_enabled": true}`.
- Optimistic-locked like the rest of the single-store surface: `404` for an unknown store id, `409` (nothing changed) when `updated_at` doesn't match the store's current value. Cheap to satisfy — the list screen already holds each store's `updated_at` from `GET /v1/stores`, so no extra fetch is needed before a toggle click.
- Success response returns fresh state — `{"id": 12, "wifi_whitelist_enabled": true, "updated_at": "..."}` — so the client can update its local copy and immediately allow another toggle without refetching.

**`PATCH /v1/stores` (bulk) becomes locked and atomic.** This reverses ADR-0004's explicit "no lock, best-effort per id" design.

- Request shape changes from a flat `ids: []int64` to `stores: [{"id": 1, "updated_at": "..."}, ...]`, each entry carrying the caller's last-known `updated_at` for that store.
- All-or-nothing: if any id doesn't exist, or any submitted `updated_at` doesn't match that store's current value, the entire request is rejected with a single `409` and nothing is written — no per-id partial application. Response body on conflict: `{"failed_ids": [2]}`.
- On success (`200`), returns fresh state for every store in the batch: `[{"id": 1, "wifi_whitelist_enabled": false, "updated_at": "..."}, ...]`.
- The old best-effort `BulkActionResult`-shaped response (`{id, success, error}` per element, partial application) is dropped for this endpoint — a request either fully applies or fully doesn't.

## Consequences

- Touches already-shipped ticket 05 code: `IsActive *bool` is removed from `patchStoreParams` and `UpdateStore` (not renamed in place — see ticket 07, which now removes rather than renames this field as part of the `is_active` → `wifi_whitelist_enabled` column rename).
- New `Service`/`Handler`/`repo.Querier` surface for the single-store toggle endpoint (new ticket).
- Ticket 09 (bulk endpoint, not yet built) is rewritten for the atomic/locked design rather than the original best-effort one.
- Data layer: the bulk endpoint now needs a pre-check pass (fetch current `(id, updated_at)` for every requested id, compare against submitted values) before running the actual `UPDATE`, all inside one transaction — a bulk `UPDATE ... WHERE id = ANY($1) RETURNING id` alone is no longer sufficient.
- `.scratch/store-wifi-credentials/spec.md` updated throughout: PATCH /v1/stores/{id} section, new single-store toggle section, rewritten bulk section, Concurrency/Errors/Data layer/Service surface/Testing Decisions.

## Superseded

This ADR does not change ADR-0001's or ADR-0004's core rulings — `wifi_whitelist_enabled` (see `CONTEXT.md`) is still a normal, admin-controlled, non-tombstone field, still untouched by the Odoo sync, still renamed and scope-narrowed as ADR-0004 describes. What's superseded is the *endpoint shape* used to set it: ADR-0001's "fold it into the existing PATCH, no new sub-resource" call, and ADR-0004's "bulk endpoint is lock-exempt, best-effort per id" call.
