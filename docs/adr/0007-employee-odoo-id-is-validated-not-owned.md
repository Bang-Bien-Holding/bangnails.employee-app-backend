---
status: accepted
---

# Employee's Odoo id is validated at write time, not fully Odoo-owned

`employee_id` (renamed/retyped to `odoo_employee_id BIGINT`, the join key to Odoo's `hr.employee`) was previously caller-supplied and never checked against Odoo at all — an employee could be created with an id Odoo has never heard of, reconciled only if an admin later ran `SyncEmployees` (see `.scratch/employee-id-model/spec.md`). Meanwhile `Store` is fully Odoo-owned from creation onward: Odoo is the source of truth for its existence, name, and city, one-way.

Employee doesn't get the full `Store` treatment. Unlike `Store`, an Employee row is created directly by an admin (name, email, username, position, store assignment are all admin input), not synced in from Odoo wholesale — Odoo only needs to vouch that the id itself is real.

## Decision

`CreateEmployee` and `UpdateEmployee` (only when `odooEmployeeId` is being changed) call Odoo to confirm a matching `hr.employee` exists, **existence-only**: a match lets the write through with the admin-submitted `fullName`/`email` written as-is; no match, or an Odoo call that fails outright (timeout, 5xx, network error), rejects the write. Odoo's response is never used to overwrite the submitted fields — that overwrite still only happens through the existing `SyncEmployees` job, unchanged.

Failing closed on an unreachable Odoo (not just a confirmed "not found") is deliberate: the entire point of adding this check is that an unverified `odoo_employee_id` shouldn't enter the system, and we can't know it's valid if we can't ask.

## Consequences

- `CreateEmployee`/`UpdateEmployee` gain a new failure mode (Odoo unreachable → write rejected) that didn't exist before; an Odoo outage now blocks employee onboarding, not just sync.
- `UpdateEmployee` only re-validates when `odooEmployeeId` changes — updating `fullName`/`email`/`positionIds`/etc. alone doesn't call Odoo.
- `SyncEmployees`'s own overwrite behavior (`fullName`, `email`, store membership) is unchanged by this decision — see [ADR-0009](/docs/adr/0009-employee-store-membership-is-many-to-many-from-odoo.md) for what it now also covers.
