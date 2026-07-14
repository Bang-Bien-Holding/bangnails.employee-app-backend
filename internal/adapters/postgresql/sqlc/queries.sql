-- name: CreateEmployee :one
INSERT INTO employees (employee_id, full_name, email, username, role)
VALUES ($1, $2, $3, $4, $5)
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
SET employee_id = $2,
    full_name = $3,
    email = $4,
    username = $5,
    role = $6,
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
-- Odoo-facing employee_id values runSync actually sends to Odoo. An id with
-- no matching row is silently omitted from the result.
SELECT employee_id FROM employees
WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: UpsertEmployees :many
-- Bulk-upserts one batch of Odoo employees (at most 50, see
-- employees.syncEmployeesParams) in a single round trip — same "(xmax = 0)"
-- trick as UpsertStores to distinguish an INSERT from an ON CONFLICT UPDATE
-- without a second query. employee_id is the shared key with Odoo (see
-- odoo.Employee), so it's the conflict target.
INSERT INTO employees (employee_id, full_name, email, username, role)
SELECT unnest(@employee_ids::varchar[]), unnest(@full_names::varchar[]), unnest(@emails::citext[]), unnest(@usernames::varchar[]), unnest(@roles::varchar[])
ON CONFLICT (employee_id) DO UPDATE
SET full_name = EXCLUDED.full_name,
    email = EXCLUDED.email,
    username = EXCLUDED.username,
    role = EXCLUDED.role,
    updated_at = now()
RETURNING id, employee_id, (xmax = 0) AS inserted;

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
-- inserted_stores vs updated_stores without a second query.
INSERT INTO store (odoo_store_id, store_name, city)
SELECT unnest(@odoo_store_ids::varchar[]), unnest(@store_names::varchar[]), unnest(@cities::varchar[])
ON CONFLICT (odoo_store_id) DO UPDATE
SET store_name = EXCLUDED.store_name,
    city = EXCLUDED.city,
    is_active = true,
    updated_at = now()
RETURNING id, odoo_store_id, (xmax = 0) AS inserted;

-- name: FindStoresNotInOdoo :many
-- Locally-created stores that have never been linked to Odoo
-- (odoo_store_id IS NULL) are deliberately excluded — only stores Odoo
-- once reported and has since stopped reporting count as deleted.
SELECT id FROM store
WHERE is_active = true
  AND odoo_store_id IS NOT NULL
  AND odoo_store_id != ALL(sqlc.arg(active_odoo_store_ids)::varchar[]);

-- name: SoftDeleteStores :execrows
UPDATE store
SET is_active = false, updated_at = now()
WHERE id = ANY(sqlc.arg(store_ids)::bigint[]);

-- name: GetStoreByID :one
-- is_active = true reuses the store-sync feature's soft-delete flag as the
-- not-found condition, rather than introducing a second deletion concept.
SELECT * FROM store
WHERE id = $1 AND is_active = true;

-- name: ListStoreWifiIPsByStoreID :many
SELECT ip_address FROM store_wifi_ip
WHERE store_id = $1
ORDER BY id;

-- name: ListStoreWifiMacsByStoreID :many
SELECT mac_address FROM store_wifi_mac
WHERE store_id = $1
ORDER BY id;

-- name: ListStores :many
-- Every store (active and inactive — the list screen's Activate toggle
-- needs to see and re-enable inactive stores), each with its current IP/MAC
-- whitelist aggregated in the same round trip rather than one query per
-- store. LATERAL subqueries (rather than a single LEFT JOIN + array_agg)
-- keep the two independent whitelists from cross-joining each other.
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
-- Updates a store's geofence and unconditionally bumps updated_at whenever
-- expected_updated_at still matches the current row — the optimistic-
-- concurrency check for the whole PATCH /v1/stores/{id} aggregate (store
-- row + wifi whitelist tables), not just the geofence. A caller that only
-- touches the wifi lists (ticket 03) still runs this with all three
-- narg columns NULL, still bumping updated_at. latitude/longitude/
-- radius_meters are nullable args: NULL means "leave this column
-- unchanged" (COALESCE keeps the existing value) rather than "clear it" —
-- the all-or-nothing geofence group is enforced by the caller, not here.
-- No returned row (pgx.ErrNoRows) means either the store doesn't exist/is
-- inactive, or expected_updated_at is stale; the caller disambiguates with
-- a follow-up GetStoreByID.
UPDATE store
SET latitude = COALESCE(sqlc.narg(latitude), latitude),
    longitude = COALESCE(sqlc.narg(longitude), longitude),
    radius_meters = COALESCE(sqlc.narg(radius_meters), radius_meters),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND is_active = true
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
