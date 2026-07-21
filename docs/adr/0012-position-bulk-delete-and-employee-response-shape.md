---
status: accepted
---

# Bulk-delete Positions is all-or-nothing; Position-first membership endpoints return the full employeeResponse shape

Issue #13 closed two remaining gaps in the Position admin screens ADR-0011 didn't cover: deleting several Positions from the list screen in one action, and the Position edit screen's Employee rows needing enough detail (name, email, store, other Positions) to recognize who's who — not just bare ids.

## Decision

**Bulk delete**: `DELETE /positions`, body `{ids: []int64}`. Handler-layer validation is strict (non-empty, unique, every id positive) rather than `employees.bulkDeleteEmployeesParams`' looser `required,min=1`. `positions.Service.BulkDeletePositions(ctx, ids) error` is all-or-nothing — one transaction, `CountPositionsByIDs` pre-check then delete, 404 (`ErrPositionNotFound`) and nothing deleted if any id doesn't exist. This is a deliberate divergence from `employees.BulkDeleteEmployees`'s best-effort `[]BulkActionResult`: the FE's confirmation dialog implies "these will all be deleted" as one intent, not a batch of independent attempts.

**Response shape**: `GET /positions/{id}/employees` and `PUT /positions/{id}/employees` both return a bare JSON array shaped exactly like `GET /employees`'s existing `employeeResponse` (`id`, `odoo_employee_id`, `full_name`, `email`, `username`, `is_active`, `position_ids`, `store_ids`, `created_at`, `updated_at`) — not the `{employee_ids: []int64}` shape originally shipped alongside ADR-0011. The `positions` package builds this with its own `EmployeeDetail`/`employeeResponse` types (field-for-field identical to `employees`') rather than importing `employees` — same "positions is a second, deliberate reader/writer of employee_positions" stance ADR-0011 already took for the write side, now extended to this read. `SetPositionEmployees`'s diff and its refetch of the resulting employee list happen inside the same transaction, so the response reflects exactly what was just written.

Supporting `repo.Querier` additions: `ListEmployeesByPositionID` (full employee rows, replacing the now-unused `ListEmployeeIDsByPositionID`) and `DeletePositions` (bulk delete by id set).

## Considered Options

- Keeping `{employee_ids: []int64}` and having the FE make a second `GET /employees` call to resolve details — rejected; issue #13 explicitly wants one call carrying everything the row needs, consistent with how the main Employee list and the "Thêm Nhân viên" picker already consume `GET /employees`.
- Exporting `employees.employeeResponse` and importing it from `positions` — rejected; keeps the shape a duplicated-on-purpose contract rather than a compile-time dependency between the two packages, matching ADR-0011's existing split.
- Best-effort bulk delete (mirroring `employees.BulkDeleteEmployees`) — rejected per issue #13's explicit user story: a stale/already-deleted id in the selection should fail the whole action, not silently skip it.

## Consequences

- Any FE code still expecting `{employee_ids: []int64}` from the position-first membership endpoints (built against the ADR-0011-era shape) needs updating to consume the array-of-`employeeResponse` shape instead.
- `positions` and `employees` each now maintain their own copy of the employee response shape; keep them in sync if `GET /employees`'s shape changes.
