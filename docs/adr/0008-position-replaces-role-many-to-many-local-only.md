---
status: accepted
---

# Position replaces role as a many-to-many, locally-owned concept

`employees.role` was a free-text `VARCHAR(50)`, required, with no enum/allow-list anywhere in the stack (DB, sqlc, or API validation). Introducing Odoo employee sync surfaced a real requirement it couldn't express: an employee can hold more than one position at once, which a single string column (or a single-valued FK) can't represent.

Odoo's `hr.employee` field mapping for this integration (`employee_id`/`id`, `full_name`/`name`, `email`/`email`, `store_id`/`x_pos_shop_ids`) has no job-title equivalent — Position isn't something Odoo has an opinion about here.

## Decision

`role` is dropped entirely. A new `positions` table (`id`, `name UNIQUE`, timestamps) holds admin-managed job titles, with full CRUD via the API. Membership is a join table, `employee_positions(employee_id, position_id)` with a composite primary key, `ON DELETE CASCADE` on both FKs — an Employee can have zero, one, or many Positions, and deleting either side just removes the assignment, never blocks or cascades further.

Position is never synced from or to Odoo. It's a purely local concept, same spirit as `wifi_whitelist_enabled` being purely admin-controlled on `Store`.

## Consequences

- `createEmployeeParams`/`updateEmployeeParams` gain an optional `positionIds: []int64`, replacing the required `role: string`. Submitting a set replaces the whole set via diff (insert added, delete removed, leave unchanged), the same pattern `CONTEXT.md` already documents for Wifi Whitelist — no dedicated single add/remove endpoint yet.
- Any `positionId` submitted is validated against `positions` before the write; an unknown id is a 400, not a raw FK-violation 500.
- `odoo.Employee` no longer carries a `Role` field; `SyncEmployees` stops touching position/role entirely (it never owned this data to begin with, in spirit — this decision makes that explicit).
