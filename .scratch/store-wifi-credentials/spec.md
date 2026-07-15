Status: ready-for-agent

# Store Wifi Credentials & Geofence Management

## Problem Statement

Employees log into the app while physically at their store. The intended primary check is: is the employee's device connected to the store's wifi? That check requires each store to have a known whitelist of its wifi network's IP addresses and MAC addresses to compare against. When a device can't be verified against wifi (e.g. wifi is down, or the check is otherwise inconclusive), the app falls back to checking the employee's GPS location against the store's geofence (latitude, longitude, radius).

Today, the `store`, `store_wifi_ip`, and `store_wifi_mac` tables exist (migration `00006_create_store_geofence.sql`), but there is no way for an admin to populate or edit them. An admin needs to view a store's current wifi whitelist and geofence, and edit both from one screen (see attached "Thêm IP & MAC Address" mockup).

## Solution

Add these endpoints to the existing `stores` module:

- `GET /v1/stores/{id}` — returns the store's details, its full IP whitelist, its full MAC whitelist, and its geofence.
- `PATCH /v1/stores/{id}` — lets an admin replace the IP whitelist and/or MAC whitelist wholesale, and/or update the geofence, in one request. Protected against lost updates from two admins editing concurrently. Does **not** set `wifi_whitelist_enabled` — see the two endpoints below. See [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md).
- `PATCH /v1/stores/{id}/wifi-whitelist-enabled` — the list screen's per-store Activate/Deactivate toggle, one boolean, its own optimistic lock. See [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md).
- `GET /v1/stores` — lists every store (wifi-enabled and wifi-disabled) with the same per-store shape as `GET /v1/stores/{id}`, for the "Cấu hình / IP & MAC Address" list screen.
- `DELETE /v1/stores/{id}/wifi-whitelist` — remove specific IP and/or MAC entries from one store's whitelist (bulk-capable, by value), for the list screen's per-entry delete action. See [ADR-0003](/docs/adr/0003-store-delete-is-per-entry-not-whole-store.md) — there is no whole-store delete via this API; superseding the whole-store hard-delete originally specced in [ADR-0002](/docs/adr/0002-store-deletion-is-hard-delete.md). (The Odoo sync does hard-delete a store under its own, separate circumstances — see Sync behavior below.)
- `PATCH /v1/stores` — atomically sets `wifi_whitelist_enabled` on an explicit list of store ids in one request, for the list screen's "deactivate all" action. See [ADR-0004](/docs/adr/0004-store-wifi-whitelist-enabled-replaces-is-active.md) and [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md).

## User Stories

1. As an admin, I want to open a store and see its current wifi IP whitelist, MAC whitelist, and geofence, so that I know what's currently configured before I change anything.
2. As an admin, I want to add one or more new IP addresses to a store by typing them comma-separated, so that I can whitelist a new router without re-typing the existing ones from scratch.
3. As an admin, I want to add one or more new MAC addresses to a store the same way, independently of the IP list.
4. As an admin, I want to remove an IP or MAC address from a store's whitelist by deleting it from the comma-separated field and saving, so that decommissioned routers stop being trusted.
5. As an admin, I want to set or update a store's latitude, longitude, and radius together, so that the GPS-fallback geofence is accurate.
6. As an admin, I want the IP list, MAC list, and geofence to be saveable in a single Save action, so that I don't need three separate screens/requests for one store's access configuration.
7. As an admin, I want to be able to update only the wifi lists without being forced to also resupply the geofence (and vice versa), so that unrelated edits don't require re-entering fields I'm not changing.
8. As an admin, I want a clear error if I type an IP or MAC address in the wrong format, so that I can fix my typo before saving instead of corrupting the whitelist.
9. As an admin, I want a clear error if I accidentally type the same IP or MAC twice in one field, so that I notice the copy-paste mistake immediately.
10. As an admin, I want the radius to be rejected if it's zero, negative, or unreasonably large, so that I can't misconfigure a geofence that either matches nowhere or matches an entire city.
11. As an admin, I want to be warned if another admin has changed this store's configuration since I loaded the page, so that my save doesn't silently overwrite their change (or vice versa).
12. As an admin, if the store I'm trying to edit doesn't exist (e.g. the id is wrong), I want a clear 404 rather than a confusing generic error.
13. As a future maintainer, I want the wifi-whitelist persistence logic to follow the same service/handler seam pattern already used by `SyncStores` and the `employees` module, so that this feature is consistent and testable the same way as the rest of the codebase.
14. As an admin, I want to see a list of every store (grouped by city), each showing its IP whitelist, MAC whitelist, and an Activate toggle, so that I can browse and manage all stores' wifi access from one screen without opening each one individually.
15. As an admin, I want to flip a store's Activate toggle on or off directly from the list, so that I can enable or disable that store's wifi-whitelist login check without a separate edit flow.
16. As an admin, I want to select one or more IP/MAC entries belonging to a store and delete them, so that I can remove decommissioned routers directly from the list screen without retyping the whole whitelist through PATCH.
17. As an admin, I want to turn off the wifi-whitelist check for a store without it also disabling the GPS/geofence fallback, so that a store with flaky wifi doesn't accidentally lock every employee out of logging in.
18. As an admin, I want to select multiple (or all) stores and turn off their wifi-whitelist check in one action, so that I can lock down wifi-based login quickly without repeating the toggle store-by-store.
19. As a future maintainer, I want `wifi_whitelist_enabled` to be purely admin-controlled and untouched by the Odoo sync, so that a scheduled sync run can never silently undo an admin's decision to disable or re-enable a store's wifi check.
20. As a future maintainer, I want a store that's permanently removed from Odoo to be fully deleted — including its wifi whitelist, with any employee still pointed at it unlinked rather than left dangling — so that there's no stale, half-alive store record left behind after a permanent closure.

## Implementation Decisions

### Endpoints

- `GET /v1/stores/{id}`
- `PATCH /v1/stores/{id}`
- `PATCH /v1/stores/{id}/wifi-whitelist-enabled`
- `DELETE /v1/stores/{id}/wifi-whitelist`
- `PATCH /v1/stores`

All live in the existing `stores` module (`internal/stores`), extending `Service`, `Handler`, and the route registration in `cmd/api.go` — no new module.

### PATCH request body

```json
{
  "updated_at": "2026-07-14T10:00:00Z",
  "ip_addresses": ["138.101.10.1", "138.101.10.2"],
  "mac_addresses": ["AA:BB:CC:DD:EE:FF"],
  "latitude": 1.1,
  "longitude": 100.2,
  "radius_meters": 50
}
```

- `updated_at`: **required** on every PATCH. Must equal the store's current `updated_at` at the moment the transaction runs, or the request is rejected — see Concurrency below.
- `ip_addresses`: optional. **Omitted** = the store's IP whitelist is left untouched. **Present** (including `[]`) = replaces the store's entire IP whitelist to match exactly what's submitted (diff against existing rows: insert values that are new, delete rows for values no longer present, leave unchanged values alone). Each element validated as IPv4 format only (no IPv6 in this pass). Duplicate values within the array are rejected (400), not silently deduped.
- `mac_addresses`: same rules as `ip_addresses`, independently. IEEE MAC-48 format.
- `latitude` / `longitude` / `radius_meters`: **all-or-nothing as a group** — if any one of the three is present, all three must be present; if none are present, the geofence is left untouched. Standard bounds for latitude (-90..90) and longitude (-180..180). `radius_meters` restricted to the range 1–1000.
- `wifi_whitelist_enabled` is **not** part of this endpoint's request body — it's not readable here as a settable field, though the response body still reports its current value (see Response body below). It's set via `PATCH /v1/stores/{id}/wifi-whitelist-enabled` (single store) or `PATCH /v1/stores` (bulk) — see the new section below and [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md).
- No `label` field on IP/MAC entries in this pass (schema column exists but stays NULL — no UI surface for it yet).
- `store_name` / `city` are not part of this body and are not editable via this endpoint.
- IP whitelist and MAC whitelist are independent lists, not paired — there is no relationship between "IP entry N" and "MAC entry N." A login attempt matches if the client's IP is in the store's IP list OR its MAC is in the store's MAC list (that matching logic itself is a separate, not-yet-built feature — out of scope here, see below).

### Response body (same shape for GET and PATCH)

```json
{
  "id": 12,
  "store_name": "Montpellier 1",
  "odoo_store_id": "M30",
  "city": "Montpellier",
  "latitude": 1.1,
  "longitude": 100.2,
  "radius_meters": 50,
  "ip_addresses": ["138.101.10.1", "138.101.10.2"],
  "mac_addresses": ["aa:bb:cc:dd:ee:ff"],
  "wifi_whitelist_enabled": true,
  "created_at": "...",
  "updated_at": "..."
}
```

`ip_addresses`/`mac_addresses` are plain string arrays — no per-entry id or metadata is exposed. Single-entry removal is addressed by value, not id (see Delete endpoint below), so no shape change is needed here to support it.

### List endpoint

`GET /v1/stores` returns a JSON array of the same per-store shape as `GET /v1/stores/{id}` above (one array element per store), ordered by `city, store_name`. No query params (no server-side search/filter/pagination) — mirrors `ListEmployees`' "return everything, let the frontend filter/group" pattern, since these lists are expected to stay small. **Includes wifi-disabled stores** — the list screen's Activate toggle needs to see and re-enable them.

### Delete endpoint

`DELETE /v1/stores/{id}/wifi-whitelist` removes specific IP and/or MAC entries from one store's whitelist — the list screen's per-entry delete action. See [ADR-0003](/docs/adr/0003-store-delete-is-per-entry-not-whole-store.md); there is no whole-store delete in this feature (that was the original, incorrect reading of the mockup, see [ADR-0002](/docs/adr/0002-store-deletion-is-hard-delete.md)).

Request body:

```json
{
  "updated_at": "2026-07-14T10:00:00Z",
  "ip_addresses": ["138.101.10.1"],
  "mac_addresses": ["AA:BB:CC:DD:EE:FF"]
}
```

- `updated_at`: **required**, same optimistic-lock rule as PATCH — must match the store's current `updated_at` or the request is rejected with `409` and nothing is deleted.
- `ip_addresses` / `mac_addresses`: both optional and independent, but at least one value must be present across the two (400 if both are empty/omitted). Same format validation as PATCH (IPv4 for `ip_addresses`, MAC-48 for `mac_addresses`) and the same in-array `unique` duplicate rejection (400).
- Deletes are **by value**, not by the tables' internal `id` columns — `store_wifi_ip`/`store_wifi_mac` already enforce `UNIQUE (store_id, ip_address)` / `UNIQUE (store_id, mac_address)`, so no per-entry id needs to be exposed anywhere.
- This is additive to PATCH's full-replace semantics for `ip_addresses`/`mac_addresses` (unchanged, still the bulk add/replace path) — this endpoint is the surgical single/multi-entry removal path.
- Bulk-capable and best-effort per entry: a submitted value not currently in the store's whitelist is reported as a failed entry but doesn't block or roll back the others (same spirit as `BulkDeleteEmployees`, keyed by value+type instead of id).
- `store.updated_at` is bumped to `now()` only if at least one entry was actually deleted — a request where every entry fails leaves `updated_at` untouched.

Response body — a JSON array of per-entry results covering both IP and MAC values from the request:

```json
[
  { "value": "138.101.10.1", "type": "ip", "success": true },
  { "value": "AA:BB:CC:DD:EE:FF", "type": "mac", "success": false, "error": "not found in whitelist" }
]
```

### Single-store wifi-whitelist-enabled toggle endpoint

`PATCH /v1/stores/{id}/wifi-whitelist-enabled` sets `wifi_whitelist_enabled` on one store — the list screen's per-row Activate/Deactivate toggle. Separate from `PATCH /v1/stores/{id}` (which no longer carries this field at all) so the UI can flip one boolean without the weight of the full-store PATCH. See [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md).

Request body:

```json
{
  "updated_at": "2026-07-14T10:00:00Z",
  "wifi_whitelist_enabled": true
}
```

- `updated_at`: **required**, same optimistic-lock rule as `PATCH /v1/stores/{id}` — must match the store's current `updated_at` or the request is rejected with `409` and nothing changes. Cheap for the caller to satisfy: the list screen already holds each store's `updated_at` from `GET /v1/stores`, so no extra fetch is needed before a toggle click.
- `wifi_whitelist_enabled`: required bool (not a pointer — this endpoint only ever does one thing).
- `store.updated_at` is bumped to `now()` on success, same as every other mutating endpoint on this aggregate.

Response body on success — fresh state, so the caller can update its local copy and immediately allow another toggle without refetching:

```json
{
  "id": 12,
  "wifi_whitelist_enabled": true,
  "updated_at": "2026-07-14T10:05:00Z"
}
```

### Bulk wifi-whitelist toggle endpoint

`PATCH /v1/stores` (collection-level, no `{id}`) atomically sets `wifi_whitelist_enabled` on an explicit set of stores in one request — the list screen's "deactivate all" (or any explicit multi-select) action. See [ADR-0004](/docs/adr/0004-store-wifi-whitelist-enabled-replaces-is-active.md) and [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md) (which reversed this endpoint's original lock-exempt, best-effort design).

Request body — each entry carries its own last-known `updated_at`, not just a bare id:

```json
{
  "stores": [
    { "id": 1, "updated_at": "2026-07-14T10:00:00Z" },
    { "id": 2, "updated_at": "2026-07-14T10:05:00Z" }
  ],
  "wifi_whitelist_enabled": false
}
```

- `stores`: required, non-empty array of `{id, updated_at}` pairs.
- `wifi_whitelist_enabled`: required bool (not a pointer — this endpoint only ever does one thing).
- **Optimistic-locked and atomic, per store.** If any submitted id doesn't exist, or any submitted `updated_at` doesn't match that store's current `updated_at`, the **entire request is rejected** and **nothing is written** — no partial application, unlike `DELETE .../wifi-whitelist`'s best-effort model. The client is expected to re-fetch (`GET /v1/stores`) and redo the action against the latest state.
- "Deactivate all stores" is a client-side concept, not a server-side one: the client sends every currently-known store id + `updated_at` pair (e.g. everything the list screen has loaded via `GET /v1/stores`). There is no wildcard/"apply to all stores unconditionally" server-side option.

Response body on success (`200`) — fresh state for every store in the batch:

```json
[
  { "id": 1, "wifi_whitelist_enabled": false, "updated_at": "2026-07-14T10:10:00Z" },
  { "id": 2, "wifi_whitelist_enabled": false, "updated_at": "2026-07-14T10:10:00Z" }
]
```

Response body on failure (`409`) — which id(s) caused the reject (either not-found or stale-lock; the client's remedy is the same either way, so the two causes aren't distinguished in the body):

```json
{ "failed_ids": [2] }
```

### Concurrency

`store.updated_at` doubles as an optimistic-lock version for the whole aggregate (the store row plus both its wifi whitelist tables). Every successful mutation — `PATCH /v1/stores/{id}` (wifi lists and/or geofence), `PATCH /v1/stores/{id}/wifi-whitelist-enabled`, `DELETE .../wifi-whitelist`, and now `PATCH /v1/stores` (bulk) — must bump `store.updated_at` to `now()`. A request whose submitted `updated_at` doesn't match the store's current `updated_at` at the time the update runs is rejected and no data is changed; the client is expected to re-fetch (`GET`) and redo its action against the latest state. **All four mutating endpoints are now optimistically locked** — see [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md), which reversed the original bulk-is-exempt design.

### Errors

- `404` — no store with this id exists. **No longer tied to `wifi_whitelist_enabled`** (see ADR-0001, ADR-0004) — a wifi-disabled store is a normal 200, not a 404, on `GET /v1/stores/{id}` and `PATCH /v1/stores/{id}`. Also applies to `DELETE .../wifi-whitelist` and `PATCH /v1/stores/{id}/wifi-whitelist-enabled` for an unknown store id.
- `409` — `updated_at` mismatch (concurrent edit). Applies to `PATCH /v1/stores/{id}`, `PATCH /v1/stores/{id}/wifi-whitelist-enabled`, and `DELETE .../wifi-whitelist`. For bulk `PATCH /v1/stores`, `409` is also used for an unknown id in the batch (not `404` — see below), with body `{"failed_ids": [...]}`; nothing in the batch is written.
- `400` — malformed IP/MAC format, duplicate value within `ip_addresses` or `mac_addresses`, partial geofence group (1 or 2 of the 3 fields present instead of 0 or 3), `radius_meters` outside 1–1000, missing `updated_at`. For `DELETE .../wifi-whitelist`: also an empty request (no values in either `ip_addresses` or `mac_addresses`). For bulk `PATCH /v1/stores`: empty/missing `stores`, or missing `wifi_whitelist_enabled`.
- `500` — anything else.

Note: an individual IP/MAC value not found during `DELETE .../wifi-whitelist` is **not** a `404` — it's a per-entry `success: false` in the `200` response body, since the request as a whole succeeded (that endpoint keeps its best-effort, partial-application model). Bulk `PATCH /v1/stores`, by contrast, is all-or-nothing: any single unknown id or stale `updated_at` in the batch fails the whole request with `409`, per [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md) — not-found and stale-lock aren't distinguished in the `409` body since the client's remedy (re-fetch and retry) is the same either way.

### Data layer

New `sqlc` queries needed (in `internal/adapters/postgresql/sqlc/queries.sql`, following the existing `unnest`-based bulk style used by `UpsertStores`):

- A query to fetch one store by id together with its current IP and MAC lists (either as one query with array aggregation, or the store row plus two small list queries — implementer's choice, whichever produces the cleanest `repo.Querier` interface).
- A query that updates `store.latitude`/`longitude`/`radius_meters` and bumps `updated_at`, conditioned on the caller-supplied `updated_at` matching the current row, returning whether a row was actually updated (0 rows updated signals the 409 case).
- A query (or pair of queries) that replaces the full `store_wifi_ip` set for a store: delete rows for that `store_id` whose `ip_address` isn't in the submitted set, then insert the submitted values that don't already exist (`ON CONFLICT DO NOTHING`), matching the `store_id, ip_address` uniqueness constraint. Same shape for `store_wifi_mac`.
- All of the above run inside a single transaction (via `service.withTx`, the same pattern `SyncStores` already uses) so the store row and both whitelist tables update atomically, and the `updated_at` bump is the last thing committed.
- A query to delete specific `store_wifi_ip` rows by `(store_id, ip_address)` value, returning affected row count (or the deleted values) so the service can report per-value success/failure. Same shape for `store_wifi_mac`. Both run in the same transaction as the conditional `updated_at` bump used by `DELETE .../wifi-whitelist` (bumped only when at least one row was actually deleted).
- A single-store query for `PATCH /v1/stores/{id}/wifi-whitelist-enabled`: `UPDATE store SET wifi_whitelist_enabled = $1, updated_at = now() WHERE id = $2 AND updated_at = $3 RETURNING id, updated_at`, same conditional-update-returning-0-rows-on-mismatch shape as the geofence query, run in its own transaction (no other tables touched).
- For bulk `PATCH /v1/stores`, since the whole batch is now atomic (see [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md)), a bare `UPDATE ... WHERE id = ANY($1)` is no longer sufficient — it can't detect a stale `updated_at` per id. Instead, inside one transaction: (1) a pre-check query fetching current `(id, updated_at)` for every submitted id (`SELECT id, updated_at FROM store WHERE id = ANY($1::bigint[])`), compared in the service against the submitted `(id, updated_at)` pairs — any missing id or any mismatch aborts before any write, reporting all mismatched/missing ids as `failed_ids`; (2) only if every id matched, a bulk `UPDATE store SET wifi_whitelist_enabled = $1, updated_at = now() WHERE id = ANY($2::bigint[]) RETURNING id, updated_at` to apply and return fresh state for the response.

### Sync behavior (ADR-0004, ADR-0005)

Two changes to the existing `SyncStores`/`UpsertStores` Odoo sync, both about keeping it from touching `wifi_whitelist_enabled`:

- `UpsertStores`' `ON CONFLICT (odoo_store_id) DO UPDATE` clause drops `is_active = true` (formerly unconditional reactivation on every sync run) — the sync no longer force-enables this flag. See ticket [07](/.scratch/store-wifi-credentials/issues/07-rename-is-active-to-wifi-whitelist-enabled.md).
- The existing soft-delete path (`FindStoresNotInOdoo` + `SoftDeleteStores`, which set `is_active = false` for a store Odoo stopped reporting) is replaced with a hard `DELETE FROM store`. `store_wifi_ip`/`store_wifi_mac` cascade automatically (`ON DELETE CASCADE`, already in place). A new migration changes the `employees.store_id` FK to `ON DELETE SET NULL`, so any employee pointed at the deleted store is unlinked atomically as part of the same delete. `FindStoresNotInOdoo`'s query drops its `is_active`/`wifi_whitelist_enabled` filter entirely, since the flag is no longer related to a store's existence. See ADR-0005 and ticket [08](/.scratch/store-wifi-credentials/issues/08-hard-delete-store-on-odoo-removal.md) — including the accepted risk of no safeguard against a transient Odoo under-report causing a premature delete.

### Service/Handler surface

- `Service` interface (in `internal/stores/types.go`) gains five methods, e.g. `GetStoreByID(ctx, id int64) (StoreDetail, error)`, `UpdateStore(ctx, id int64, params UpdateStoreParams) (StoreDetail, error)`, `DeleteWifiWhitelistEntries(ctx, id int64, params DeleteWifiWhitelistParams) ([]WifiWhitelistDeleteResult, error)`, `SetStoreWifiWhitelistEnabled(ctx, id int64, params SetWifiWhitelistEnabledParams) (StoreDetail, error)` (new — the single-store toggle), and `BulkSetWifiWhitelistEnabled(ctx, params BulkSetWifiWhitelistEnabledParams) ([]StoreDetail, error)` (signature changed — no longer returns a best-effort `[]BulkActionResult`; returns fresh state for every store on success, or the sentinel conflict error with `failed_ids` on failure).
- `UpdateStore`'s params (`patchStoreParams` today) **drop** the `wifi_whitelist_enabled`/`is_active` field entirely — undoing that part of ticket 05's already-shipped work (commit `9ee8d86`). See ticket 07, revised to remove rather than rename this field.
- New sentinel errors alongside the existing `ErrSyncInProgress`: a not-found error and a concurrency-conflict error, mapped to 404 and 409 respectively in the handler the same way `employees` maps its sentinel errors to status codes. Reused by the delete endpoint **and now by both wifi-whitelist-enabled endpoints** (single-store and bulk) — bulk's conflict error carries the `failed_ids` list.
- Validation (`go-playground/validator`) on the PATCH params struct, called the same way every other handler in this codebase already does (`validate.Struct` → 400 with the validator's error text on failure): `dive` + format tags for IP/MAC elements, `unique` tag for in-array duplicate rejection, `required_with`-style grouping for the lat/long/radius trio, numeric range tags for radius. The delete endpoint's params struct reuses the same IP/MAC format and `unique` tags, plus a `required_without_all`-style rule requiring at least one of `ip_addresses`/`mac_addresses` to be non-empty. The single-store toggle endpoint's params struct: `updated_at` tagged `required`, `wifi_whitelist_enabled` tagged `required` (non-pointer bool). The bulk endpoint's params struct: `stores` tagged `required,min=1,dive` (each element's `id` and `updated_at` also `required`), `wifi_whitelist_enabled` tagged `required` (non-pointer bool).
- `WifiWhitelistDeleteResult` remains a local type in `internal/stores/types.go` for the delete endpoint's best-effort per-entry results. The old `BulkActionResult`-shaped per-id result type is **no longer used** by the bulk toggle endpoint now that it's atomic — its success response is `[]StoreDetail`-shaped (or an equivalent local response type), its failure response is `{failed_ids: []int64}`.

## Testing Decisions

- Good tests here assert observable behavior (HTTP status + response body shape, or the arguments a service passes to its `repo.Querier`), never internal implementation details like which private helper function ran.
- **Service seam** (`internal/stores/service_test.go`): test `GetStoreByID` and `UpdateStore` against a mocked `repo.Querier` (`sqlcmocks.MockQuerier`, already generated in this repo) via the existing `newTestService` helper that stubs `withTx`. Cover: omitted list left untouched (repo replace-call not invoked) vs. present list (including empty) triggering a replace call with the right diff inputs; geofence group omitted vs. present; `updated_at` mismatch surfacing as the conflict sentinel error with zero side effects; not-found mapping. `UpdateStore` no longer has `wifi_whitelist_enabled` omit-vs-present coverage — that field is gone from its params. Also test `DeleteWifiWhitelistEntries`: a mix of present/absent IP and MAC values in one call reported correctly per entry, `updated_at` mismatch surfacing as the conflict sentinel with zero side effects, `updated_at` left untouched when every entry fails, not-found mapping for an unknown store id. Also test `SetStoreWifiWhitelistEnabled` (new): success bumps `updated_at` and returns fresh state, `updated_at` mismatch surfaces the conflict sentinel with zero side effects, not-found mapping for an unknown store id. Also test `BulkSetWifiWhitelistEnabled` (rewritten for atomic behavior): all-ids-match-and-exist succeeds, returning fresh state for every store; one stale `updated_at` among several ids aborts the whole call with zero side effects and reports every mismatched/missing id in `failed_ids`; one unknown id among several existing ones also aborts the whole call (not a partial success); empty `stores` list is a validation-level 400, not reachable at the service seam. Also update `TestStoreService_SyncStores`: a store present in Odoo no longer gets `wifi_whitelist_enabled` force-set to `true`; a store `FindStoresNotInOdoo` reports is hard-deleted (assert the delete query is called with the right ids) rather than soft-deleted. Prior art: `internal/stores/service_test.go`'s existing `TestStoreService_SyncStores`, and `internal/employees/service_test.go`.
- **Handler seam** (`internal/stores/handlers_test.go`): test `GetStoreByID`/`PatchStore` HTTP handlers against a mocked `Service` (`MockService`, regenerate via the existing `go:generate mockgen -source=types.go` directive in `types.go`). Cover: validation failures (bad IP/MAC format, duplicate array values, partial geofence group, radius out of range, missing `updated_at`) each returning 400; sentinel-error-to-status-code mapping (404, 409); success response body matches the documented JSON shape, with `wifi_whitelist_enabled` still present as a read-only field but absent from what the handler accepts as writable. Also test the new delete handler: 400 for an empty request/bad format/duplicate values, 404 for unknown store id, 409 for `updated_at` mismatch, and the success response's per-entry result array shape. Also test the new single-store toggle handler: 400 for missing `updated_at`/`wifi_whitelist_enabled`, 404 for unknown store id, 409 for `updated_at` mismatch, success response shape (`id`, `wifi_whitelist_enabled`, `updated_at`). Also test the rewritten bulk handler: 400 for empty/missing `stores` or missing `wifi_whitelist_enabled`, 409 with the `failed_ids` body shape for any unknown id or stale `updated_at` in the batch, success response's per-store array shape (`id`, `wifi_whitelist_enabled`, `updated_at`). Prior art: `internal/stores/handlers_test.go`'s existing `TestStoreHandler_SyncStores`, and `internal/employees/handlers_test.go`.
- The employees-nulling (`employees.store_id` → `NULL`) and wifi-whitelist-cascade behavior of a hard store delete are DB-constraint-level guarantees (`ON DELETE SET NULL`, `ON DELETE CASCADE`), not application logic — out of scope for the mocked-`repo.Querier` service seam. This repo has no DB-backed integration test infrastructure to add a seam for them; covered by the migration being declarative plus manual/staging verification.

## Out of Scope

- The actual login-time matching logic that checks a request's IP/MAC against a store's whitelist, or falls back to geofence distance — this spec only covers admin management of the whitelist/geofence data, not its consumption.
- Any authentication/authorization guard on these endpoints — no such middleware exists anywhere in this codebase yet for any endpoint; this feature follows the same (currently absent) pattern as everything else rather than introducing one unilaterally.
- IPv6 support for `ip_addresses`.
- A `label` per IP/MAC entry (schema column exists, left NULL; no UI for it yet).
- Per-entry **edit** of a single existing IP/MAC row (e.g. changing an IP's value in place) — only delete-by-value is in scope; editing a value is remove-then-re-add via PATCH or two separate calls.
- Whole-store deletion **via this API** — no admin-facing endpoint deletes a store; a store's own lifecycle (creation, renaming, permanent closure) stays owned by the one-way Odoo sync; see [ADR-0003](/docs/adr/0003-store-delete-is-per-entry-not-whole-store.md). (The sync itself does hard-delete a store under its own circumstances — see Sync behavior above and [ADR-0005](/docs/adr/0005-store-removal-from-odoo-is-hard-delete.md) — that's a sync-owned decision, not something this API exposes.)
- A safeguard against a transient Odoo under-report causing a premature store delete (consecutive-miss threshold, time-based grace window, sync-size circuit breaker, two-step flag-then-confirm) — explicitly accepted as a risk for now, see ADR-0005.
- Editing `store_name` or `city` via this endpoint.
- Any general store list/search endpoint beyond fetching one store by id.
- Pagination of the IP/MAC whitelist (lists are expected to be small, on the order of a handful of routers per store).

## Further Notes

- The mockup screen ("Thêm IP & MAC Address") shows `store_name`/`store_code` as read-only display and a "Tiếp tục tạo IP Address" ("keep adding") checkbox — both are frontend-only concerns (the store is already known from navigation context; the checkbox just controls whether the form stays open after a successful save) and don't change this API's contract.
- `store.is_active` was originally reused as the not-found condition for `GET`/`PATCH` (soft-delete, from the Odoo-sync feature). That was reversed once the list/Activate-toggle screen needed to fetch and reactivate inactive stores — see ADR-0001. It was then renamed to `wifi_whitelist_enabled` and its scope narrowed to gating only the wifi-whitelist check — see ADR-0004. There is no store-level deletion concept exposed via this API — see ADR-0003; via this API, the only "delete" removes IP/MAC whitelist entries, never the store row itself. The Odoo sync, separately, does hard-delete a store when Odoo stops reporting it — see ADR-0005.
- ADR-0001 originally put the Activate toggle on `PATCH /v1/stores/{id}` and explicitly rejected a separate sub-resource endpoint for it. That call is reversed: the toggle now lives on its own locked `PATCH /v1/stores/{id}/wifi-whitelist-enabled`, and the bulk `PATCH /v1/stores` — originally lock-exempt and best-effort per id (ADR-0004) — is now optimistic-locked and atomic. See [ADR-0006](/docs/adr/0006-single-store-wifi-toggle-endpoint-bulk-becomes-atomic.md).
