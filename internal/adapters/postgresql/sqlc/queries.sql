-- name: CreateEmployee :one
INSERT INTO employees (odoo_employee_id, full_name, email, username)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetEmployeeByID :one
SELECT * FROM employees
WHERE id = $1;

-- name: GetEmployeeByEmail :one
SELECT * FROM employees
WHERE email = $1;

-- name: GetEmployeeByUsername :one
SELECT * FROM employees
WHERE username = $1;

-- name: ListEmployees :many
SELECT * FROM employees
ORDER BY id;

-- name: UpdateEmployee :one
UPDATE employees
SET odoo_employee_id = $2,
    full_name = $3,
    email = $4,
    username = $5,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetEmployeePassword :execrows
UPDATE employees
SET password = $2,
    updated_at = now()
WHERE id = $1;

-- name: SetEmployeeActive :execrows
UPDATE employees
SET is_active = $2,
    updated_at = now()
WHERE id = $1;

-- name: DeleteEmployee :execrows
DELETE FROM employees
WHERE id = $1;

-- name: ListEmployeeIDsByIDs :many
-- Translates the internal ids a SyncEmployees caller supplies into the
-- Odoo-facing odoo_employee_id values runSync actually sends to Odoo. An id
-- with no matching row is silently omitted from the result.
SELECT odoo_employee_id FROM employees
WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: UpsertEmployees :many
-- Bulk-upserts one batch of Odoo employees (at most 50, see
-- employees.syncEmployeesParams) in a single round trip — same "(xmax = 0)"
-- trick as UpsertStores to distinguish an INSERT from an ON CONFLICT UPDATE
-- without a second query. odoo_employee_id is the shared key with Odoo (see
-- odoo.Employee), so it's the conflict target.
INSERT INTO employees (odoo_employee_id, full_name, email, username)
SELECT unnest(@odoo_employee_ids::bigint[]), unnest(@full_names::varchar[]), unnest(@emails::citext[]), unnest(@usernames::varchar[])
ON CONFLICT (odoo_employee_id) DO UPDATE
SET full_name = EXCLUDED.full_name,
    email = EXCLUDED.email,
    username = EXCLUDED.username,
    updated_at = now()
RETURNING id, odoo_employee_id, (xmax = 0) AS inserted;

-- name: CreatePasswordResetToken :one
INSERT INTO password_reset_tokens (employee_id, token_hash, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: RedeemPasswordResetToken :one
-- Atomically claims a valid, unused token: the UPDATE's row lock ensures
-- only one concurrent caller can match the WHERE clause and get a row back,
-- so CompleteActivation can't be raced into redeeming the same token twice.
-- Callers pass the SHA-256 digest of the bearer token, not the raw value.
UPDATE password_reset_tokens
SET used_at = now()
WHERE token_hash = $1
  AND used_at IS NULL
  AND expires_at > now()
RETURNING *;

-- name: UpsertStores :many
-- Bulk-upserts one page of Odoo stores in a single round trip. "(xmax = 0)"
-- is Postgres' standard trick for distinguishing an INSERT from an
-- ON CONFLICT UPDATE in the same statement: xmax is only set by an UPDATE,
-- so a fresh row's xmax is 0. The store-sync service uses it to report
-- inserted_stores vs updated_stores without a second query. updated_at only
-- advances when store_name/city actually changed — store.updated_at also
-- doubles as the admin-facing optimistic-lock version for the whole store
-- aggregate, so a sync run that finds no real change must not invalidate a
-- concurrently-in-flight admin edit's lock token.
INSERT INTO store (odoo_store_id, store_name, city)
SELECT unnest(@odoo_store_ids::varchar[]), unnest(@store_names::varchar[]), unnest(@cities::varchar[])
ON CONFLICT (odoo_store_id) DO UPDATE
SET store_name = EXCLUDED.store_name,
    city = EXCLUDED.city,
    updated_at = CASE
        WHEN store.store_name IS DISTINCT FROM EXCLUDED.store_name
          OR store.city IS DISTINCT FROM EXCLUDED.city
        THEN now()
        ELSE store.updated_at
    END
RETURNING id, odoo_store_id, (xmax = 0) AS inserted;

-- name: FindStoresNotInOdoo :many
-- Locally-created stores that have never been linked to Odoo
-- (odoo_store_id IS NULL) are deliberately excluded — only stores Odoo
-- once reported and has since stopped reporting count as deleted. No
-- wifi_whitelist_enabled filter (see ADR-0005) — that flag is unrelated to a
-- store's existence, so filtering on it would permanently orphan a
-- wifi-disabled store once it left Odoo, since it would never be selected
-- here as stale.
SELECT id FROM store
WHERE odoo_store_id IS NOT NULL
  AND odoo_store_id != ALL(sqlc.arg(active_odoo_store_ids)::varchar[]);

-- name: DeleteStores :execrows
-- Hard-deletes stores Odoo no longer reports (see ADR-0005) — replaces the
-- former SoftDeleteStores. store_wifi_ip/store_wifi_mac cascade
-- automatically (ON DELETE CASCADE, migration 00006).
DELETE FROM store
WHERE id = ANY(sqlc.arg(store_ids)::bigint[]);

-- name: GetStoreByID :one
-- No wifi_whitelist_enabled filter — see ADR-0001/ADR-0004, it's a normal
-- editable field, not a soft-delete tombstone, so a wifi-disabled store is
-- still a normal fetch here, not a 404.
SELECT * FROM store
WHERE id = $1;

-- name: ListStoreWifiIPsByStoreID :many
SELECT ip_address FROM store_wifi_ip
WHERE store_id = $1
ORDER BY id;

-- name: ListStoreWifiMacsByStoreID :many
SELECT mac_address FROM store_wifi_mac
WHERE store_id = $1
ORDER BY id;

-- name: ListStores :many
-- Every store (wifi-enabled and wifi-disabled — the list screen's Activate
-- toggle needs to see and re-enable wifi-disabled stores), each with its
-- current IP/MAC whitelist aggregated in the same round trip rather than one
-- query per store. LATERAL subqueries (rather than a single LEFT JOIN +
-- array_agg) keep the two independent whitelists from cross-joining each
-- other.
SELECT
    sqlc.embed(s),
    COALESCE(ip.ip_addresses, '{}')::inet[] AS ip_addresses,
    COALESCE(mac.mac_addresses, '{}')::macaddr[] AS mac_addresses
FROM store s
LEFT JOIN LATERAL (
    SELECT array_agg(ip_address ORDER BY id) AS ip_addresses
    FROM store_wifi_ip
    WHERE store_id = s.id
) ip ON true
LEFT JOIN LATERAL (
    SELECT array_agg(mac_address ORDER BY id) AS mac_addresses
    FROM store_wifi_mac
    WHERE store_id = s.id
) mac ON true
ORDER BY s.city, s.store_name;

-- name: UpdateStoreGeofence :one
-- Updates a store's geofence, and unconditionally bumps updated_at whenever
-- expected_updated_at still matches the current row — the
-- optimistic-concurrency check for the whole PATCH /v1/stores/{id} aggregate
-- (store row + wifi whitelist tables), not just the geofence. A caller that
-- only touches the wifi lists (ticket 03) or touches neither (ticket 06's
-- delete endpoint, to bump updated_at alone) still runs this with the
-- geofence narg columns NULL, still bumping updated_at.
-- latitude/longitude/radius_meters are nullable args: NULL means "leave this
-- column unchanged" (COALESCE keeps the existing value) rather than "clear
-- it" — the all-or-nothing geofence group is enforced by the caller, not
-- here. wifi_whitelist_enabled is not part of this query at all — see
-- ADR-0006, it's set exclusively via its own dedicated endpoints. No
-- returned row (pgx.ErrNoRows) means either the store doesn't exist, or
-- expected_updated_at is stale; the caller disambiguates with a follow-up
-- GetStoreByID.
UPDATE store
SET latitude = COALESCE(sqlc.narg(latitude), latitude),
    longitude = COALESCE(sqlc.narg(longitude), longitude),
    radius_meters = COALESCE(sqlc.narg(radius_meters), radius_meters),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND updated_at = sqlc.arg(expected_updated_at)
RETURNING *;

-- name: DeleteStoreWifiIPsNotIn :exec
-- Half of the "replace this store's IP whitelist to match ip_addresses
-- exactly" diff (paired with InsertStoreWifiIPs) — deletes whatever's
-- currently there but no longer submitted. "!= ALL(...)" over an empty
-- ip_addresses array is vacuously true for every row, so submitting []
-- correctly clears the whole whitelist rather than being a no-op.
DELETE FROM store_wifi_ip
WHERE store_id = sqlc.arg(store_id)
  AND ip_address != ALL(sqlc.arg(ip_addresses)::inet[]);

-- name: InsertStoreWifiIPs :exec
-- Other half of the replace diff: inserts whatever's newly submitted.
-- ON CONFLICT DO NOTHING is what makes values already present in both the
-- old and new set stay untouched rather than being deleted and reinserted.
INSERT INTO store_wifi_ip (store_id, ip_address)
SELECT sqlc.arg(store_id), unnest(sqlc.arg(ip_addresses)::inet[])
ON CONFLICT (store_id, ip_address) DO NOTHING;

-- name: DeleteStoreWifiMacsNotIn :exec
-- MAC-address counterpart of DeleteStoreWifiIPsNotIn.
DELETE FROM store_wifi_mac
WHERE store_id = sqlc.arg(store_id)
  AND mac_address != ALL(sqlc.arg(mac_addresses)::macaddr[]);

-- name: InsertStoreWifiMacs :exec
-- MAC-address counterpart of InsertStoreWifiIPs.
INSERT INTO store_wifi_mac (store_id, mac_address)
SELECT sqlc.arg(store_id), unnest(sqlc.arg(mac_addresses)::macaddr[])
ON CONFLICT (store_id, mac_address) DO NOTHING;

-- name: DeleteStoreWifiIPsByValue :many
-- Deletes specific store_wifi_ip rows by value, not the table's internal id
-- (see ADR-0003 — a value unambiguously identifies the row within a store
-- thanks to the UNIQUE (store_id, ip_address) constraint) — the surgical
-- per-entry removal path for DELETE /v1/stores/{id}/wifi-whitelist, as
-- opposed to DeleteStoreWifiIPsNotIn's whole-list replace. RETURNING the
-- deleted values (rather than a row count) lets the caller report each
-- submitted value's success/failure independently and best-effort: a
-- submitted value not present in the whitelist simply doesn't come back in
-- this set, without erroring or blocking the rest of the batch.
DELETE FROM store_wifi_ip
WHERE store_id = sqlc.arg(store_id)
  AND ip_address = ANY(sqlc.arg(ip_addresses)::inet[])
RETURNING ip_address;

-- name: DeleteStoreWifiMacsByValue :many
-- MAC-address counterpart of DeleteStoreWifiIPsByValue.
DELETE FROM store_wifi_mac
WHERE store_id = sqlc.arg(store_id)
  AND mac_address = ANY(sqlc.arg(mac_addresses)::macaddr[])
RETURNING mac_address;

-- name: SetStoreWifiWhitelistEnabled :one
-- PATCH /v1/stores/{id}/wifi-whitelist-enabled's single query — same
-- conditional-update-returning-0-rows-on-mismatch shape as
-- UpdateStoreGeofence, but scoped to just this one column since this
-- endpoint only ever does one thing (see ADR-0006). No returned row
-- (pgx.ErrNoRows) means either the store doesn't exist, or
-- expected_updated_at is stale; the caller disambiguates with a follow-up
-- GetStoreByID.
UPDATE store
SET wifi_whitelist_enabled = sqlc.arg(wifi_whitelist_enabled),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND updated_at = sqlc.arg(expected_updated_at)
RETURNING id, updated_at;

-- name: GetStoresByIDsForUpdate :many
-- Pre-check pass for bulk PATCH /v1/stores (see ADR-0006): fetches every
-- submitted id's current (id, updated_at) inside the same transaction as the
-- bulk UPDATE that follows, so the service can compare against the caller's
-- submitted pairs before writing anything — any missing id or stale
-- updated_at aborts the whole request. FOR UPDATE locks these rows so a
-- concurrent mutation can't slip in between this check and the bulk UPDATE.
SELECT id, updated_at FROM store
WHERE id = ANY(sqlc.arg(store_ids)::bigint[])
FOR UPDATE;

-- name: BulkSetStoreWifiWhitelistEnabled :many
-- Applies the bulk PATCH /v1/stores write once GetStoresByIDsForUpdate has
-- confirmed every id exists and every updated_at matched — see ADR-0006.
-- RETURNING fresh state for every affected store so the handler can build
-- the success response without a follow-up fetch.
UPDATE store
SET wifi_whitelist_enabled = sqlc.arg(wifi_whitelist_enabled),
    updated_at = now()
WHERE id = ANY(sqlc.arg(store_ids)::bigint[])
RETURNING id, wifi_whitelist_enabled, updated_at;

-- name: CreatePosition :one
INSERT INTO positions (name)
VALUES ($1)
RETURNING *;

-- name: ListPositions :many
SELECT * FROM positions
ORDER BY name;

-- name: UpdatePosition :one
UPDATE positions
SET name = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeletePosition :execrows
DELETE FROM positions
WHERE id = $1;

-- name: CountPositionsByIDs :one
-- Used to validate a submitted set of position ids in one round trip: if the
-- count of matching rows is less than the count of distinct submitted ids,
-- at least one id doesn't reference a real position (see ADR-0008 — this
-- must be a clear client error, not a raw FK-violation 500).
SELECT count(*) FROM positions
WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ListPositionIDsByEmployeeID :many
SELECT position_id FROM employee_positions
WHERE employee_id = $1
ORDER BY position_id;

-- name: ListPositionIDsByEmployeeIDs :many
-- Bulk counterpart of ListPositionIDsByEmployeeID for ListEmployees — one
-- round trip for every employee's position ids, grouped client-side by
-- employee_id rather than aggregated here (keeps this query a plain
-- row-per-pair scan, same shape as the single-employee version).
SELECT employee_id, position_id FROM employee_positions
WHERE employee_id = ANY(sqlc.arg(employee_ids)::bigint[])
ORDER BY employee_id, position_id;

-- name: DeleteEmployeePositionsNotIn :exec
-- Half of the "replace this employee's position set to match position_ids
-- exactly" diff (paired with InsertEmployeePositions) — deletes whatever's
-- currently assigned but no longer submitted. "!= ALL(...)" over an empty
-- position_ids array is vacuously true for every row, so submitting []
-- correctly clears the employee's entire position set rather than being a
-- no-op (see ADR-0008).
DELETE FROM employee_positions
WHERE employee_id = sqlc.arg(employee_id)
  AND position_id != ALL(sqlc.arg(position_ids)::bigint[]);

-- name: InsertEmployeePositions :exec
-- Other half of the replace diff: inserts whatever's newly submitted.
-- ON CONFLICT DO NOTHING is what makes assignments already present in both
-- the old and new set stay untouched rather than being deleted and
-- reinserted.
INSERT INTO employee_positions (employee_id, position_id)
SELECT sqlc.arg(employee_id), unnest(sqlc.arg(position_ids)::bigint[])
ON CONFLICT (employee_id, position_id) DO NOTHING;
