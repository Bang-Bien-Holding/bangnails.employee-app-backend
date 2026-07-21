Status: resolved (published as GitHub issue #15)

**Update:** issue #13 changed the response shape documented below —
`GET`/`PUT /v1/positions/{id}/employees` now return the full `employeeResponse`
array (same shape as `GET /employees`), not `{employee_ids: []int64}`. See
ADR-0012.

# Position CRUD + position-first employee membership endpoints (ADR-0011)

## Problem Statement

Admins manage Positions (local job titles like "Technician", "Manager") and which Employees hold them. Today, changing an Employee's Position membership only works Employee-first, through `POST /v1/employees` and `PUT /v1/employees/{id}`'s `positionIds` field (ADR-0008) — an admin has to already be looking at one Employee to change their Positions.

A new screen needs to work the other way round: an admin opens one Position and edits the full set of Employees who hold it, from the Position's own edit form. There is no API to read or write a Position's Employee set directly, and no dedicated CRUD surface for Positions themselves (create, list, rename, delete) either — Positions so far only existed as a side effect of Employee writes.

## Solution

Add a `positions` API surface with full CRUD for the Position resource itself, plus two Position-first membership endpoints that read and whole-set-replace a Position's Employees — mirroring the existing Employee-first whole-set-replace convention (ADR-0008) in the other direction, per ADR-0011.

## User Stories

1. As an admin, I want to create a new Position with a name, so that I can define a new job title Employees can hold.
2. As an admin, I want creating a Position with a name that already exists to fail with a clear error, so that I don't end up with duplicate job titles.
3. As an admin, I want to list all Positions, so that I can see every job title currently defined.
4. As an admin, I want to rename a Position, so that I can correct or update a job title without losing its Employee assignments.
5. As an admin, I want renaming a Position to a name that collides with another Position to fail with a clear error, so duplicates can't be created via rename either.
6. As an admin, I want renaming a Position that doesn't exist to return a 404, so I get a clear signal instead of a silent no-op.
7. As an admin, I want to delete a Position, so that I can remove a job title that's no longer in use.
8. As an admin, I want deleting a Position that doesn't exist to return a 404.
9. As an admin, I want deleting a Position to also remove its Employee memberships, so no orphaned membership rows are left behind (existing `ON DELETE CASCADE` behavior from ADR-0008, unchanged by this work).
10. As an admin, I want to open one Position and see the full list of Employees currently assigned to it, so I know who holds that job title today.
11. As an admin, I want reading a Position's Employees for a Position that doesn't exist to return a 404.
12. As an admin, I want a Position with no Employees to show an empty list, not null or an error.
13. As an admin, I want to submit a Position's complete desired Employee set in one save action from the Position edit form, so the Position's membership matches exactly what I selected — Employees no longer in the set are unassigned, newly added ones are assigned, and unchanged ones are left alone.
14. As an admin, I want submitting a Position's Employee set for a Position that doesn't exist to return a 404 instead of silently creating rows.
15. As an admin, I want submitting a Position's Employee set that includes an id that isn't a real Employee to fail with a clear 400 error, not a raw database error, so I know exactly which kind of mistake I made.
16. As an admin, I want submitting an empty Employee set to succeed and leave the Position with zero Employees, so I can fully unassign a Position in one save.
17. As an admin, I want the whole-set replace to be atomic — if any part of the save fails (e.g. an unknown Employee id slips through a race with a delete), none of the change should apply, so the Position's membership never ends up in a partially-applied state.
18. As an admin, I want renaming a Position to stay a separate action from editing its Employees, so the two concerns (the Position's own field vs. its membership) don't get tangled into one request contract — the frontend calls both endpoints together on save, but they remain independent APIs.

## Implementation Decisions

- **Resource**: `Position` — `id`, `name` (its only field besides timestamps), `created_at`, `updated_at`. Local-only, admin-managed, never synced from Odoo (per `CONTEXT.md`).
- **New package**: `internal/positions`, following the existing `internal/employees` / `internal/stores` shape — `Handler` (HTTP layer), `Service` interface + `service` implementation, `types.go` for request/response DTOs and sentinel errors.
- **Position CRUD**:
  - `POST /v1/positions` — body `{name: string}`, `name` required (trimmed, non-empty). Returns the created `Position` (201).
  - `GET /v1/positions` — returns all Positions (200), no pagination/filtering.
  - `PUT /v1/positions/{id}` — body `{name: string}`, required. Position has exactly one editable field, so a rename is the whole resource. 404 if `id` doesn't exist.
  - `DELETE /v1/positions/{id}` — 204 on success, 404 if `id` doesn't exist. Cascades to `employee_positions` via existing `ON DELETE CASCADE` (ADR-0008) — no new cascade logic needed.
- **Position-first membership endpoints (ADR-0011)**:
  - `GET /v1/positions/{id}/employees` — 404 if the Position doesn't exist; otherwise returns `{employee_ids: []int64}`, always a non-null (possibly empty) array.
  - `PUT /v1/positions/{id}/employees` — body `{employeeIds: []int64}` (nil/omitted treated as empty). Whole-set replace via diff: delete `employee_positions` rows for this Position not in the submitted set, then insert the newly-submitted ones, both inside one transaction so a failing insert rolls back the delete. Returns `{employee_ids: []int64}` reflecting the new set.
  - This mirrors `PUT /v1/employees/{id}`'s `positionIds` whole-set replace (ADR-0008) in the opposite direction — Position-first instead of Employee-first.
  - Rejected alternative (ADR-0010): single-row `POST/DELETE /positions/{id}/employees/{employeeId}` assign/unassign — rejected because the actual screen submits one full desired Employee set per save (one "Lưu" button), not incremental per-row calls.
- **Ownership of the write**: `SetPositionEmployees` calls `repo.Querier` directly rather than routing through `employees.Service`, making `positions` a second, deliberate writer of `employee_positions` alongside `employees` — a Position-first diff and an Employee-first diff are different operations that happen to touch the same join table, not the same operation duplicated (ADR-0011). Keep both writers in sync if the join table's shape changes.
- **Validation order in `SetPositionEmployees`**, inside one transaction:
  1. Confirm the Position exists (`ErrPositionNotFound` / 404 if not).
  2. Validate every submitted employee id is a real Employee via one count query (`CountEmployeesByIDs` vs. distinct submitted count) — `ErrUnknownEmployeeID` / 400 if any is unknown. Empty/nil id set short-circuits this check (always valid — a Position with no Employees).
  3. Delete rows for this Position not in the submitted set.
  4. Insert the newly-submitted rows (skipped if the set is empty).
  5. A foreign-key violation surfacing from the insert (position or employee deleted in the narrow race window between step 1/2 and step 4) is translated to `ErrPositionNotFound` or `ErrUnknownEmployeeID` by constraint name (`employee_positions_position_id_fkey` / `employee_positions_employee_id_fkey`), never leaked as a raw 500.
- **Error mapping** (sentinel errors in `types.go`, translated to HTTP status in the handler via `errors.Is`):
  - `ErrPositionNotFound` → 404 (`UpdatePosition`, `DeletePosition`, `GetPositionEmployees`, `SetPositionEmployees`)
  - `ErrPositionNameAlreadyExists` → 409 (`CreatePosition`, `UpdatePosition`; translated from Postgres unique-violation `23505` on `positions_name_key`)
  - `ErrUnknownEmployeeID` → 400 (`SetPositionEmployees`; translated from Postgres FK-violation `23503`, or from the pre-check count mismatch)
  - Anything else → 500, logged via `slog.Error`, generic body to the client.
- **Request validation**: struct-tag validation (`go-playground/validator`) at the handler layer before the service is ever called — `name` required or `employeeIds` elements `unique,dive,required` (no duplicate or zero ids).
- **Response shape convention**: `employee_ids` (snake_case) on the membership endpoints; `positionResponse` fields in the existing camelCase-free style matching `repo.Position` directly. `EmployeeIDs` is always serialized non-nil (empty array, not `null`) for a Position with no Employees — same convention as `EmployeeDetail.PositionIDs`.

## Testing Decisions

- Two seams, matching the existing pattern in `internal/employees` and `internal/stores`:
  1. **Handler tests** (`internal/positions/handlers_test.go`) — table-driven, `httptest.ResponseRecorder` against `Handler` methods directly, with a `gomock`-generated `MockService` standing in for `Service`. Cover: happy path, validation failures caught before the service is called (assert the mock has no expectations set), and each sentinel-error → HTTP-status mapping.
  2. **Service tests** (`internal/positions/service_test.go`) — table-driven, against a mocked `repo.Querier` (via the same `mockgen -source=types.go` generation the package already declares). Cover: happy path, `pgx.ErrNoRows` → `ErrPositionNotFound`, Postgres unique/FK violation → the matching sentinel error by constraint name, and the transactional delete+insert diff behavior of `SetPositionEmployees` (via the `withTx` seam already used for tests, which calls `fn` against the mocked `Querier` directly instead of a real pool transaction).
- Only external behavior is tested — HTTP status/body from the handler layer, and the `repo.Querier` calls + returned domain values/errors from the service layer — not internal implementation details.
- Prior art: `internal/employees/handlers_test.go` and `internal/employees/service_test.go` already test the mirror-image Employee-first `positionIds` whole-set replace (ADR-0008) with the same two-seam, table-driven, gomock-based shape.

## Out of Scope

- Any change to how an Employee's Position set is edited Employee-first (`POST/PUT /v1/employees...`) — unchanged, still ADR-0008's whole-set replace.
- Single-row assign/unassign endpoints — considered and rejected twice (ADR-0010, reaffirmed by ADR-0011).
- Pagination, filtering, or sorting on `GET /v1/positions` or `GET /v1/positions/{id}/employees`.
- Exposing any field on Position beyond `name`/`id`/timestamps (e.g. no `description` field exists or is planned).
- Odoo sync for Positions — Position is and remains local-only, never sourced from Odoo.
- Any frontend/UI work for the Position edit form itself — this spec covers only the backend API surface it calls.

## Further Notes

- This spec documents work already implemented on `feat/position-employees-endpoint` (commits `6a590b6` "feat: add position-first employee membership endpoints (ADR-0011)" and `6cd8539` "fix: translate FK violations in SetPositionEmployees to domain errors"), not yet merged to `main`. It's being filed for the record / as the spec of truth alongside ADR-0011, not as forward-looking planning.
- Also published as [GitHub issue #15](https://github.com/Bang-Bien-Holding/bangnails.employee-app-backend/issues/15), labeled `ready-for-agent`, per this repo's issue-tracker convention.
- Relevant ADRs: ADR-0008 (Position replaces Role, many-to-many, local-only — establishes the join table and Employee-first replace), ADR-0010 (single-assign/unassign endpoint — rejected), ADR-0011 (Position-first whole-set replace — accepted, governs this spec).
