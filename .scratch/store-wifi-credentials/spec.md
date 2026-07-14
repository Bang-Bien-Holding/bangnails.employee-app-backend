Status: ready-for-agent

# Store Wifi Credentials & Geofence Management

## Problem Statement

Employees log into the app while physically at their store. The intended primary check is: is the employee's device connected to the store's wifi? That check requires each store to have a known whitelist of its wifi network's IP addresses and MAC addresses to compare against. When a device can't be verified against wifi (e.g. wifi is down, or the check is otherwise inconclusive), the app falls back to checking the employee's GPS location against the store's geofence (latitude, longitude, radius).

Today, the `store`, `store_wifi_ip`, and `store_wifi_mac` tables exist (migration `00006_create_store_geofence.sql`), but there is no way for an admin to populate or edit them. An admin needs to view a store's current wifi whitelist and geofence, and edit both from one screen (see attached "Thêm IP & MAC Address" mockup).

## Solution

Add two endpoints to the existing `stores` module:

- `GET /v1/stores/{id}` — returns the store's details, its full IP whitelist, its full MAC whitelist, and its geofence.
- `PATCH /v1/stores/{id}` — lets an admin replace the IP whitelist and/or MAC whitelist wholesale, and/or update the geofence, in one request. Protected against lost updates from two admins editing concurrently.

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
12. As an admin, if the store I'm trying to edit doesn't exist (e.g. was deleted or the id is wrong), I want a clear 404 rather than a confusing generic error.
13. As a future maintainer, I want the wifi-whitelist persistence logic to follow the same service/handler seam pattern already used by `SyncStores` and the `employees` module, so that this feature is consistent and testable the same way as the rest of the codebase.

## Implementation Decisions

### Endpoints

- `GET /v1/stores/{id}`
- `PATCH /v1/stores/{id}`

Both live in the existing `stores` module (`internal/stores`), extending `Service`, `Handler`, and the route registration in `cmd/api.go` — no new module.

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
  "is_active": true,
  "created_at": "...",
  "updated_at": "..."
}
```

`ip_addresses`/`mac_addresses` are plain string arrays — no per-entry id or metadata is exposed, since nothing in this pass addresses a single whitelist entry individually (removal happens only via the full-replace PATCH, not a per-entry delete).

### Concurrency

`store.updated_at` doubles as an optimistic-lock version for the whole aggregate (the store row plus both its wifi whitelist tables). Every successful PATCH — regardless of whether it touched the wifi lists, the geofence, or both — must bump `store.updated_at` to `now()`. A PATCH whose submitted `updated_at` doesn't match the store's current `updated_at` at the time the update runs is rejected with `409 Conflict` and no data is changed. The client is expected to re-fetch (`GET`) and redo its edit against the latest state.

### Errors

- `404` — store id does not exist (or `is_active = false`, consistent with `store`'s existing soft-delete semantics from the Odoo sync feature).
- `409` — `updated_at` mismatch (concurrent edit).
- `400` — malformed IP/MAC format, duplicate value within `ip_addresses` or `mac_addresses`, partial geofence group (1 or 2 of the 3 fields present instead of 0 or 3), `radius_meters` outside 1–1000, missing `updated_at`.
- `500` — anything else.

### Data layer

New `sqlc` queries needed (in `internal/adapters/postgresql/sqlc/queries.sql`, following the existing `unnest`-based bulk style used by `UpsertStores`):

- A query to fetch one store by id together with its current IP and MAC lists (either as one query with array aggregation, or the store row plus two small list queries — implementer's choice, whichever produces the cleanest `repo.Querier` interface).
- A query that updates `store.latitude`/`longitude`/`radius_meters` and bumps `updated_at`, conditioned on the caller-supplied `updated_at` matching the current row, returning whether a row was actually updated (0 rows updated signals the 409 case).
- A query (or pair of queries) that replaces the full `store_wifi_ip` set for a store: delete rows for that `store_id` whose `ip_address` isn't in the submitted set, then insert the submitted values that don't already exist (`ON CONFLICT DO NOTHING`), matching the `store_id, ip_address` uniqueness constraint. Same shape for `store_wifi_mac`.
- All of the above run inside a single transaction (via `service.withTx`, the same pattern `SyncStores` already uses) so the store row and both whitelist tables update atomically, and the `updated_at` bump is the last thing committed.

### Service/Handler surface

- `Service` interface (in `internal/stores/types.go`) gains two methods, e.g. `GetStoreByID(ctx, id int64) (StoreDetail, error)` and `UpdateStore(ctx, id int64, params UpdateStoreParams) (StoreDetail, error)`.
- New sentinel errors alongside the existing `ErrSyncInProgress`: a not-found error and a concurrency-conflict error, mapped to 404 and 409 respectively in the handler the same way `employees` maps its sentinel errors to status codes.
- Validation (`go-playground/validator`) on the PATCH params struct, called the same way every other handler in this codebase already does (`validate.Struct` → 400 with the validator's error text on failure): `dive` + format tags for IP/MAC elements, `unique` tag for in-array duplicate rejection, `required_with`-style grouping for the lat/long/radius trio, numeric range tags for radius.

## Testing Decisions

- Good tests here assert observable behavior (HTTP status + response body shape, or the arguments a service passes to its `repo.Querier`), never internal implementation details like which private helper function ran.
- **Service seam** (`internal/stores/service_test.go`): test `GetStoreByID` and `UpdateStore` against a mocked `repo.Querier` (`sqlcmocks.MockQuerier`, already generated in this repo) via the existing `newTestService` helper that stubs `withTx`. Cover: omitted list left untouched (repo replace-call not invoked) vs. present list (including empty) triggering a replace call with the right diff inputs; geofence group omitted vs. present; `updated_at` mismatch surfacing as the conflict sentinel error with zero side effects; not-found mapping. Prior art: `internal/stores/service_test.go`'s existing `TestStoreService_SyncStores`, and `internal/employees/service_test.go`.
- **Handler seam** (`internal/stores/handlers_test.go`): test `GetStoreByID`/`PatchStore` HTTP handlers against a mocked `Service` (`MockService`, regenerate via the existing `go:generate mockgen -source=types.go` directive in `types.go`). Cover: validation failures (bad IP/MAC format, duplicate array values, partial geofence group, radius out of range, missing `updated_at`) each returning 400; sentinel-error-to-status-code mapping (404, 409); success response body matches the documented JSON shape. Prior art: `internal/stores/handlers_test.go`'s existing `TestStoreHandler_SyncStores`, and `internal/employees/handlers_test.go`.

## Out of Scope

- The actual login-time matching logic that checks a request's IP/MAC against a store's whitelist, or falls back to geofence distance — this spec only covers admin management of the whitelist/geofence data, not its consumption.
- Any authentication/authorization guard on these endpoints — no such middleware exists anywhere in this codebase yet for any endpoint; this feature follows the same (currently absent) pattern as everything else rather than introducing one unilaterally.
- IPv6 support for `ip_addresses`.
- A `label` per IP/MAC entry (schema column exists, left NULL; no UI for it yet).
- Per-entry delete-by-id or edit of a single existing IP/MAC row — superseded by the full-replace PATCH semantics.
- Editing `store_name` or `city` via this endpoint.
- Any general store list/search endpoint beyond fetching one store by id.
- Pagination of the IP/MAC whitelist (lists are expected to be small, on the order of a handful of routers per store).

## Further Notes

- The mockup screen ("Thêm IP & MAC Address") shows `store_name`/`store_code` as read-only display and a "Tiếp tục tạo IP Address" ("keep adding") checkbox — both are frontend-only concerns (the store is already known from navigation context; the checkbox just controls whether the form stays open after a successful save) and don't change this API's contract.
- `store.is_active` (soft-delete, from the Odoo-sync feature) is reused as the not-found condition for `GET`/`PATCH` rather than introducing a second deletion concept for stores.
