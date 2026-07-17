# 04 — GET /v1/stores: list all stores

**What to build:** A new `GET /v1/stores` endpoint returning every store — active and inactive — in the same per-store shape as `GET /v1/stores/{id}` (ticket 01), for the "Cấu hình / IP & MAC Address" list screen. No query params: no server-side search, filter, or pagination, mirroring `ListEmployees`' "return everything, frontend filters/groups" pattern.

**Blocked by:** 01 (reuses the response shape and the store+wifi-list aggregation query)

**Status:** done

- [ ] `GET /v1/stores` is registered in the router.
- [ ] Response is a JSON array, one element per store, each using the exact same shape as `GET /v1/stores/{id}`'s response body (`id`, `store_name`, `odoo_store_id`, `city`, `latitude`, `longitude`, `radius_meters`, `ip_addresses`, `mac_addresses`, `is_active`, `created_at`, `updated_at`).
- [ ] Inactive stores (`is_active = false`) are included in the list, not filtered out — the Activate toggle needs to see and re-enable them.
- [ ] Results are ordered by `city`, then `store_name`.
- [ ] No query parameters are accepted or required — the endpoint always returns the full set.
- [ ] `Service` gains a `ListStores(ctx) ([]StoreDetail, error)` method; the new `repo.Querier` query aggregates each store's IP/MAC lists the same way `GetStoreByID`'s does, without N+1 per-store round trips.
- [ ] Service seam: test against a mocked `repo.Querier` that the returned list includes inactive stores and preserves the query's ordering.
- [ ] Handler seam: test that `200` returns the documented array shape, including a mix of active/inactive stores and stores with empty wifi lists.
