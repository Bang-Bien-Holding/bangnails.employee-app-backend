# 01 — GET /v1/stores/{id}: view a store's wifi whitelist & geofence

**What to build:** An admin can fetch a single store by id and see its full current state in one response: store metadata (name, Odoo code, city, active flag, timestamps), its complete IP whitelist, its complete MAC whitelist, and its geofence (latitude, longitude, radius). This is the read side that the "Thêm IP & MAC Address" screen loads before the admin edits anything, and it establishes the response shape and not-found handling that ticket 02 and 03 build on.

**Blocked by:** None — can start immediately

**Status:** ready-for-agent

- [ ] `GET /v1/stores/{id}` is registered in the router alongside the existing `/stores/syncs` route.
- [ ] A new sqlc query (or small set of queries) fetches one store row together with its current `store_wifi_ip` and `store_wifi_mac` entries, following the existing `unnest`-based bulk query style already used by `UpsertStores`.
- [ ] The response body includes: `id`, `store_name`, `odoo_store_id`, `city`, `latitude`, `longitude`, `radius_meters`, `ip_addresses` (plain string array), `mac_addresses` (plain string array), `is_active`, `created_at`, `updated_at`. A store with no whitelist entries yet returns empty arrays, not null or an error.
- [ ] A store whose id doesn't exist, or whose `is_active` is `false` (reusing the existing soft-delete semantics from the store-sync feature), returns `404`.
- [ ] Service seam: a test against a mocked `repo.Querier` covers the found case (correct data mapped into the response type) and the not-found case (sentinel error returned).
- [ ] Handler seam: a test against a mocked `Service` covers `200` with the documented JSON shape and `404` mapping, following the pattern in `internal/stores/handlers_test.go` / `internal/employees/handlers_test.go`.
