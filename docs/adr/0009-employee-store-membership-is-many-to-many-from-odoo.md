---
status: accepted
---

# Employee-store membership is many-to-many, sourced from Odoo

`employees.store_id` was a single nullable FK to `store(id)` (migration `00007`, `ON DELETE SET NULL` per `00009`/[ADR-0005](/docs/adr/0005-store-removal-from-odoo-is-hard-delete.md)). The Odoo field mapping for this integration maps our store assignment to `hr.employee.x_pos_shop_ids` — an Odoo `x_`-prefixed, `_ids`-suffixed field, which is Odoo's naming convention for a many2many relation. Bangnails staff genuinely can and do work at more than one store, so a single FK column silently can't represent that.

Unlike [Position](/docs/adr/0008-position-replaces-role-many-to-many-local-only.md), this relationship *is* Odoo-owned: staff are assigned to stores in Odoo, not by an admin in this system.

## Decision

`employees.store_id` is dropped entirely, replaced by `employee_stores(employee_id, store_id)` — composite primary key, `ON DELETE CASCADE` on both FKs. It's populated exclusively by the existing `SyncEmployees` job: `odoo.Employee` gains `StoreIDs []int` (from `x_pos_shop_ids`), each resolved to our internal `store.id` via `store.odoo_store_id` (the same join key `SyncStores` already uses), then diffed per employee — insert newly-added pairs, delete no-longer-present ones, same pattern as Wifi Whitelist. Nothing outside sync writes to this table; `CreateEmployee`/`UpdateEmployee` never touch store membership.

## Consequences

- Supersedes the FK mechanism ADR-0005 introduced: a store's hard delete no longer nulls `employees.store_id` (the column is gone) — it cascades through `employee_stores` instead. ADR-0005's actual ruling (store removal from Odoo is a hard delete) is untouched; only that one consequence of it is superseded.
- Any code path that read/wrote `employees.store_id` directly (handlers, `repo.Employee.StoreID`, employee response DTOs) moves to reading `employee_stores` instead — a per-employee list of store ids rather than one nullable value.
- `SyncEmployees` gains a real dependency on `SyncStores` having run first (or at least on `store.odoo_store_id` already being populated) — an Odoo store id with no matching `store.odoo_store_id` row can't be resolved to an internal `store.id` yet. Unresolvable ids are logged and skipped, consistent with how an unrecognized `employee_id` is already handled today.
