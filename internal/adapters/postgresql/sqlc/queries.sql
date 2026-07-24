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
-- Optional-filter search for issue #28: every sqlc.narg(...) IS NULL check
-- skips that facet entirely when the caller omitted the corresponding query
-- parameter (employees.ListEmployeesFilter's zero value) — the standard
-- "$n IS NULL means skip this filter" static-SQL pattern (sqlc requires
-- compile-time SQL, so filters can't be built by string concatenation).
-- position_ids and store_ids are independent, OR-within/AND-across facets —
-- never paired (ADR-0008, ADR-0009; issue #28 user story 7) — and each is
-- matched via a sub-SELECT against its join table rather than a JOIN, so an
-- employee with zero positions/stores still matches when that facet's
-- filter is omitted (user stories 8-9). Sorted case-insensitively by
-- full_name (user story 10) since Postgres' default collation is
-- case-sensitive.
SELECT * FROM employees
WHERE (sqlc.narg(q)::text IS NULL OR full_name ILIKE '%' || replace(replace(replace(sqlc.narg(q)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%' OR email ILIKE '%' || replace(replace(replace(sqlc.narg(q)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%')
  AND (sqlc.narg(position_ids)::bigint[] IS NULL OR id IN (SELECT employee_id FROM employee_positions WHERE position_id = ANY(sqlc.narg(position_ids)::bigint[])))
  AND (sqlc.narg(store_ids)::bigint[] IS NULL OR id IN (SELECT employee_id FROM employee_stores WHERE store_id = ANY(sqlc.narg(store_ids)::bigint[])))
  AND (sqlc.narg(odoo_employee_ids)::bigint[] IS NULL OR odoo_employee_id = ANY(sqlc.narg(odoo_employee_ids)::bigint[]))
  AND (sqlc.narg(is_active)::bool IS NULL OR is_active = sqlc.narg(is_active)::bool)
ORDER BY lower(full_name) ASC;

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
-- Bulk-updates one batch of Odoo employees (at most 50, see
-- employees.syncEmployeesParams) in a single round trip. Update-only, by
-- design (see ADR-0008/ADR-0009): an odoo_employee_id with no matching row
-- — not yet admin-created, or deleted since this batch was fetched from
-- Odoo — is silently ignored rather than inserted, so sync can never
-- (re)create an employee row (which would otherwise need a placeholder,
-- Odoo-blind username). Only CreateEmployee ever inserts a row.
UPDATE employees
SET full_name = data.full_name,
    email = data.email,
    updated_at = now()
FROM (
    SELECT unnest(@odoo_employee_ids::bigint[]) AS odoo_employee_id,
           unnest(@full_names::varchar[]) AS full_name,
           unnest(@emails::citext[]) AS email
) AS data
WHERE employees.odoo_employee_id = data.odoo_employee_id
RETURNING employees.id, employees.odoo_employee_id, false AS inserted;

-- name: InvalidatePasswordResetTokensByEmployeeID :exec
-- Marks every still-outstanding (unused) token for an Employee as consumed.
-- Called by issuePasswordResetToken before it inserts a new token, so at
-- most one issued token is ever redeemable per Employee at a time.
UPDATE password_reset_tokens
SET used_at = now()
WHERE employee_id = $1
  AND used_at IS NULL;

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
-- advances when store_name actually changed — store.updated_at also
-- doubles as the admin-facing optimistic-lock version for the whole store
-- aggregate, so a sync run that finds no real change must not invalidate a
-- concurrently-in-flight admin edit's lock token. city isn't part of this
-- upsert at all: Odoo's pos.shop model has no city field yet, so sync must
-- not touch (and blank out) whatever city a store already has locally.
INSERT INTO store (odoo_store_id, store_name)
SELECT unnest(@odoo_store_ids::varchar[]), unnest(@store_names::varchar[])
ON CONFLICT (odoo_store_id) DO UPDATE
SET store_name = EXCLUDED.store_name,
    updated_at = CASE
        WHEN store.store_name IS DISTINCT FROM EXCLUDED.store_name
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
--
-- Optional-filter search for issues #32/#33/#34: every sqlc.narg(...) IS
-- NULL check skips that facet entirely when the caller omitted the
-- corresponding query parameter (stores.ListStoresFilter's zero value), same
-- "$n IS NULL means skip this filter" pattern as ListEmployees (issue #28).
-- store_name and city are independent, AND-across facets, each a
-- case-insensitive substring match; city never matches a store with no city
-- set, since NULL city ILIKE anything is NULL (falsy), not true.
-- wifi_whitelist_enabled (issue #33) is an exact-match boolean facet, AND'd
-- with the rest. odoo_store_ids (issue #34) is OR-within/AND-across, same
-- shape as ListStoresByOdooStoreIDs' odoo_store_id = ANY(...) — matched
-- against s.odoo_store_id, VARCHAR not bigint (unlike ListEmployees'
-- odoo_employee_ids), so a NULL odoo_store_id never matches, same as city.
-- Sorted case-insensitively by store_name (issue #32), replacing the former
-- ORDER BY s.city, s.store_name.
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
WHERE (sqlc.narg(store_name)::text IS NULL OR s.store_name ILIKE '%' || replace(replace(replace(sqlc.narg(store_name)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%')
  AND (sqlc.narg(city)::text IS NULL OR s.city ILIKE '%' || replace(replace(replace(sqlc.narg(city)::text, '\', '\\'), '%', '\%'), '_', '\_') || '%')
  AND (sqlc.narg(wifi_whitelist_enabled)::bool IS NULL OR s.wifi_whitelist_enabled = sqlc.narg(wifi_whitelist_enabled)::bool)
  AND (sqlc.narg(odoo_store_ids)::varchar[] IS NULL OR s.odoo_store_id = ANY(sqlc.narg(odoo_store_ids)::varchar[]))
ORDER BY lower(s.store_name) ASC;

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

-- name: DeletePositions :execrows
-- Bulk-delete counterpart of DeletePosition (see issue #13) — deletes every
-- submitted id in one statement. BulkDeletePositions pre-checks all ids
-- exist via CountPositionsByIDs inside the same transaction, so this is
-- only the "delete" half of an all-or-nothing count-check-then-delete, not
-- an existence check itself.
DELETE FROM positions
WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: CountPositionsByIDs :one
-- Used to validate a submitted set of position ids in one round trip: if the
-- count of matching rows is less than the count of distinct submitted ids,
-- at least one id doesn't reference a real position (see ADR-0008 — this
-- must be a clear client error, not a raw FK-violation 500).
SELECT count(*) FROM positions
WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: GetPositionByID :one
-- Existence check for the position-first endpoints (ADR-0011) — GET/PUT
-- /positions/{id}/employees both need to 404 on an unknown position id
-- before touching employee_positions.
SELECT * FROM positions
WHERE id = $1;

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

-- name: CountEmployeesByIDs :one
-- Position-first counterpart of CountPositionsByIDs: validates a submitted
-- set of employee ids in one round trip for PUT /positions/{id}/employees
-- (see ADR-0011) — a count short of the distinct submitted ids means at
-- least one id isn't a real employee.
SELECT count(*) FROM employees
WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ListEmployeesByPositionID :many
-- Position-first counterpart of ListPositionIDsByEmployeeID, for
-- GET/PUT /positions/{id}/employees — returns full employee rows (not just
-- ids) so the position package can build the same employeeResponse shape
-- GET /employees already returns (see issue #13).
SELECT e.* FROM employees e
JOIN employee_positions ep ON ep.employee_id = e.id
WHERE ep.position_id = $1
ORDER BY e.id;

-- name: DeleteEmployeePositionsByPositionIDNotIn :exec
-- Position-first half of the "replace this position's employee set to match
-- employee_ids exactly" diff (paired with InsertPositionEmployees, see
-- ADR-0011) — deletes whatever's currently assigned but no longer
-- submitted. Same "!= ALL(...) over empty is vacuously true" behavior as
-- DeleteEmployeePositionsNotIn: submitting [] clears the position's entire
-- employee set rather than being a no-op.
DELETE FROM employee_positions
WHERE position_id = sqlc.arg(position_id)
  AND employee_id != ALL(sqlc.arg(employee_ids)::bigint[]);

-- name: InsertPositionEmployees :exec
-- Position-first half of the replace diff: inserts whatever's newly
-- submitted. ON CONFLICT DO NOTHING is what makes assignments already
-- present in both the old and new set stay untouched rather than being
-- deleted and reinserted.
INSERT INTO employee_positions (employee_id, position_id)
SELECT unnest(sqlc.arg(employee_ids)::bigint[]), sqlc.arg(position_id)
ON CONFLICT (employee_id, position_id) DO NOTHING;

-- name: ListStoresByOdooStoreIDs :many
-- Resolves a batch of Odoo store ids (matched against store's VARCHAR
-- odoo_store_id join key — the same one SyncStores already uses) to this
-- system's internal store.id, for employee sync to map each employee's
-- Odoo store membership onto local store rows (see ADR-0009). An
-- odoo_store_id with no matching row is simply absent from the result; the
-- caller (runSync) logs and skips those rather than failing the sync.
SELECT id, odoo_store_id FROM store
WHERE odoo_store_id = ANY(sqlc.arg(odoo_store_ids)::varchar[]);

-- name: ListStoreIDsByEmployeeID :many
SELECT store_id FROM employee_stores
WHERE employee_id = $1
ORDER BY store_id;

-- name: ListStoresForLoginByEmployeeID :many
-- Every Store an Employee belongs to, each with its current IP and MAC
-- whitelists — Login's (issues #21/#22) candidate set for the IP/Geofence/MAC
-- presence check (ADR-0013). Ordered by store id, the deterministic
-- tie-break auth.matchStore relies on when more than one Store could
-- plausibly match. wifi_whitelist_enabled comes along on the embedded
-- Store row so the caller can gate the IP/MAC tiers per store without a
-- second query; geofence columns are on the same row for the same reason.
SELECT
    sqlc.embed(s),
    COALESCE(ip.ip_addresses, '{}')::inet[] AS ip_addresses,
    COALESCE(mac.mac_addresses, '{}')::macaddr[] AS mac_addresses
FROM store s
JOIN employee_stores es ON es.store_id = s.id
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
WHERE es.employee_id = sqlc.arg(employee_id)
ORDER BY s.id;

-- name: RecordFailedLoginAttempt :one
-- Atomically increments failed_login_attempts and, only once the new count
-- reaches sqlc.arg(threshold), sets locked_until to sqlc.arg(locked_until)
-- (computed in Go as now + the lockout duration, so the duration itself
-- stays a Go constant, not a SQL literal — see auth.loginLockoutDuration).
-- Callers (auth.Service.Login) only ever reach this query once they've
-- already confirmed locked_until is unset or in the past — an
-- Employee still inside an active lockout window never gets here. Given
-- that, "consecutive" resets the moment a prior lockout has fully elapsed:
-- if locked_until is already in the past, this failed attempt is attempt 1
-- of a fresh run, not a continuation of the old count — otherwise a single
-- failure right after a lockout expires would instantly re-lock the
-- Employee instead of giving them a full new run of attempts.
UPDATE employees e
SET failed_login_attempts = calc.new_count,
    locked_until = CASE
        WHEN calc.new_count >= sqlc.arg(threshold)::int THEN sqlc.arg(locked_until)::timestamptz
        ELSE NULL
    END
FROM (
    SELECT emp.id,
        CASE
            WHEN emp.locked_until IS NOT NULL AND emp.locked_until <= now() THEN 1
            ELSE emp.failed_login_attempts + 1
        END AS new_count
    FROM employees emp
    WHERE emp.id = sqlc.arg(id)
) AS calc
WHERE e.id = calc.id
RETURNING e.*;

-- name: ResetFailedLoginAttempts :exec
-- Clears an Employee's lockout state on a successful password check
-- (Login), regardless of whether the subsequent presence check goes on to
-- match a Store — a correct password is what "consecutive failed attempts"
-- counts, not the presence outcome.
UPDATE employees
SET failed_login_attempts = 0,
    locked_until = NULL
WHERE id = sqlc.arg(id);

-- name: UpsertSession :one
-- Login's single-active-Session write (ADR-0014): inserts a new Session for
-- employee_id, or, if one already exists (UNIQUE employee_id), atomically
-- replaces it in place — the "a new Login invalidates any Session already
-- open for that Employee" rule, in one round trip rather than a
-- delete-then-insert. On the DO UPDATE path, last_heartbeat_at/
-- consecutive_failures are reset explicitly (the INSERT path already gets
-- them from the column defaults) — a re-issued Session for an Employee who
-- had one open before starts Heartbeat's silence/failure tracking clean,
-- not carrying over the old Session's state (issue #23).
INSERT INTO sessions (employee_id, store_id, token_hash, expires_at)
VALUES (sqlc.arg(employee_id), sqlc.arg(store_id), sqlc.arg(token_hash), sqlc.arg(expires_at))
ON CONFLICT (employee_id) DO UPDATE
SET store_id = EXCLUDED.store_id,
    token_hash = EXCLUDED.token_hash,
    issued_at = now(),
    expires_at = EXCLUDED.expires_at,
    last_heartbeat_at = now(),
    consecutive_failures = 0
RETURNING *;

-- name: DeleteSessionByTokenHash :execrows
-- Logout: ends the Session matching this bearer token's hash immediately,
-- no re-authentication required. A hash matching no row (already logged
-- out, expired and reaped, or never valid) still returns 0 rows rather than
-- an error — Logout is idempotent, see auth.Service.Logout. Also reused by
-- Heartbeat (issue #23) to end a Session on expiry/silence/left_premises.
DELETE FROM sessions
WHERE token_hash = sqlc.arg(token_hash);

-- name: GetSessionByTokenHash :one
-- Heartbeat's (issue #23) and ValidateSession's (issue #25) lookup of the
-- Session a bearer token currently names. A hash matching no row — never
-- valid, already logged out/expired-and-ended, or (for a stale token still
-- held by a device) superseded by a newer Login's UpsertSession overwriting
-- the same employee_id row in place — is pgx.ErrNoRows; the caller can't
-- distinguish those cases from this query alone, which is why Heartbeat
-- reports the generic logged_out_elsewhere reason for all of them (see
-- auth.Service.Heartbeat).
SELECT * FROM sessions
WHERE token_hash = sqlc.arg(token_hash);

-- name: RecordHeartbeatSuccess :one
-- A Heartbeat (issue #23) whose presence recheck matched the Session's Store:
-- resets consecutive_failures to 0 (a pass between failures forgives them,
-- per ADR-0014) and refreshes last_heartbeat_at so the 90s silence backstop's
-- clock restarts. Does not touch expires_at — the 12-hour cap is absolute,
-- not extended by a passing Heartbeat.
UPDATE sessions
SET last_heartbeat_at = now(),
    consecutive_failures = 0
WHERE token_hash = sqlc.arg(token_hash)
RETURNING *;

-- name: RecordHeartbeatFailure :one
-- A Heartbeat (issue #23) whose presence recheck did not match the Session's
-- Store: still refreshes last_heartbeat_at (a failed check is still a check
-- that arrived — only true silence should trip the 90s backstop) and
-- increments consecutive_failures by one. The caller (auth.Service.Heartbeat)
-- ends the Session once the returned count reaches 2.
UPDATE sessions
SET last_heartbeat_at = now(),
    consecutive_failures = consecutive_failures + 1
WHERE token_hash = sqlc.arg(token_hash)
RETURNING *;

-- name: IsEmployeeAdmin :one
-- The single centralized "does this Employee hold the Admin Position" check
-- ADR-0015 calls for — an exact, case-insensitive match on the Position
-- name "Admin", not a pattern. Shared by Login's presence-check bypass
-- (issue #24) and the admin-gating middleware (issue #25); no other call
-- site should reimplement this comparison.
SELECT EXISTS (
    SELECT 1
    FROM employee_positions ep
    JOIN positions p ON p.id = ep.position_id
    WHERE ep.employee_id = sqlc.arg(employee_id)
    AND LOWER(p.name) = 'admin'
);

-- name: ListStoreIDsByEmployeeIDs :many
-- Bulk counterpart of ListStoreIDsByEmployeeID for ListEmployees — same
-- plain row-per-pair shape as ListPositionIDsByEmployeeIDs, grouped
-- client-side by employee_id.
SELECT employee_id, store_id FROM employee_stores
WHERE employee_id = ANY(sqlc.arg(employee_ids)::bigint[])
ORDER BY employee_id, store_id;

-- name: DeleteEmployeeStoresNotIn :exec
-- Half of the "replace this employee's store membership to match store_ids
-- exactly" diff (paired with InsertEmployeeStores) — deletes whatever's
-- currently assigned but no longer present in Odoo's resolved set. Unlike
-- employee_positions' diff pair, only runSync ever calls this — store
-- membership is Odoo-owned, never admin-writable (see ADR-0009).
DELETE FROM employee_stores
WHERE employee_id = sqlc.arg(employee_id)
  AND store_id != ALL(sqlc.arg(store_ids)::bigint[]);

-- name: InsertEmployeeStores :exec
-- Other half of the replace diff: inserts whatever's newly resolved.
-- ON CONFLICT DO NOTHING is what makes assignments already present in both
-- the old and new set stay untouched rather than being deleted and
-- reinserted.
INSERT INTO employee_stores (employee_id, store_id)
SELECT sqlc.arg(employee_id), unnest(sqlc.arg(store_ids)::bigint[])
ON CONFLICT (employee_id, store_id) DO NOTHING;
