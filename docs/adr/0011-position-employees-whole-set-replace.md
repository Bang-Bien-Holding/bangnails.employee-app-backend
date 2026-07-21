---
status: accepted
---

# Position-first employee membership uses whole-set replace, not ADR-0010's single-row design

ADR-0010 rejected a single-row `POST/DELETE /positions/{id}/employees/{employeeId}` endpoint because no screen worked Position-first — viewing one Position and adding/removing Employees from it directly. That screen now exists: the Position edit form lists the Position's currently assigned Employees, offers an "add employee" picker, and saves everything through one action.

## Decision

`GET /positions/{id}/employees` (read) and `PUT /positions/{id}/employees` (write, body `{employeeIds: []int64}`) are added. The write is a whole-set replace via diff — insert added, delete removed — the same pattern ADR-0008 already uses for an Employee's Position set (`PUT /employees/{id}` with `positionIds`), not ADR-0010's single-row add/remove design. Renaming a Position stays on the existing `PUT /positions/{id}` (name only); the FE calls both endpoints together on save.

Write logic lives in the `positions` package, calling `repo.Querier` directly for `employee_positions` — a second writer alongside `employees.Service`. This is a deliberate split, not drift: a Position-first diff (given a Position, compute which Employees to add/remove) and an Employee-first diff (given an Employee, compute which Positions to add/remove) are different operations that happen to touch the same join table, not the same operation duplicated.

## Considered Options

- ADR-0010's single-row assign/unassign design — rejected again; the concrete screen turned out to submit a full desired Employee set per save (one "Lưu" button), not incremental per-row calls, so whole-set replace fits the actual UI better.
- Routing the new write through `employees.Service` to keep one writer of `employee_positions` — rejected; that service's methods are shaped around "replace this Employee's full Position list," so reusing it would only add indirection, not avoid a second diff direction being written somewhere.

## Consequences

- `employee_positions` has two writers now (`positions` and `employees` packages), each owning one diff direction. Keep both in sync with this ADR if the join table's shape changes.
- ADR-0010's rejection stands as the historical record for why the single-row design specifically was rejected; this ADR answers the same "is there a Position-first need yet" question with a yes, but a different shape.
- Bulk delete of Positions is unrelated to this decision — cascade behavior on `employee_positions` when a Position is deleted still comes from ADR-0008's `ON DELETE CASCADE`.
