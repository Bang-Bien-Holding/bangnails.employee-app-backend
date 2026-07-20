---
status: rejected
---

# Position single-assign/unassign endpoint — proposed, implemented, then reverted

ADR-0008 introduced `employee_positions` as a many-to-many join table but only wired whole-set replacement into `PUT /employees/{id}`, explicitly deferring "no dedicated single add/remove endpoint yet." Issue #12 proposed closing that gap with `POST /positions/{id}/employees/{employeeId}` and `DELETE /positions/{id}/employees/{employeeId}` — idempotent single-row assign/unassign, 404 on an unknown position or employee id, logic living in the `positions` package calling `repo.Querier` directly rather than `employees.Service`.

The endpoints were built, tested, and reviewed on `feat/position-single-assign-endpoint` before being reconsidered.

## Decision

Not adopted, for now. The branch was deleted (both local and remote) and the work is not merged.

## Reasoning

The motivating gap doesn't currently exist in the product: the only screen that edits an Employee's Positions is the employee create/edit form, which already knows and submits that Employee's **complete** desired Position set in one call — `POST /v1/employees` and `PUT /v1/employees/{id}` both accept `positionIds: []int64` and whole-set-replace via diff (ADR-0008). A single-row assign/unassign endpoint only earns its keep if some screen or integration works **Position-first** — e.g. viewing one Position and adding/removing Employees from it directly, without knowing any one Employee's full Position list. No such screen exists or is planned today.

Building the endpoint ahead of that need would add API surface, a second writer package for `employee_positions` (see ADR-0008's original single-writer intent), and test/maintenance burden for a use case nobody calls yet.

## Consequences

- `PUT /v1/employees/{id}` (and `POST /v1/employees`) remain the only way to change an Employee's Position membership — full-set replace via `positionIds`, unchanged from ADR-0008.
- If a Position-first screen or integration becomes concrete later, revisit issue #12 — the implementation (single-row conflict-safe insert/delete `repo.Querier` methods, existence-check-then-write service logic, `positions`-package ownership) is a known-working design; it just isn't needed yet.
- No change to `CONTEXT.md`'s Position entry — it continues to document only the one assignment mechanism that actually exists.

**Update:** a Position-first screen did become concrete — see ADR-0011, which answers the "is this needed yet" question with a yes but picks whole-set replace over this ADR's single-row design (the screen submits a full desired Employee set per save, not incremental per-row edits). This ADR's rejection of the single-row design specifically still stands.
