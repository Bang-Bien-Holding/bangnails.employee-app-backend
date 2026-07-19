Status: resolved — see [ADR-0007](/docs/adr/0007-employee-odoo-id-is-validated-not-owned.md) and `.scratch/employee-odoo-integration/spec.md`. The two-id design is kept, matching this doc's own recommendation below; the business key is renamed `odoo_employee_id` and retyped to `BIGINT`, and is now validated (existence-only, fail-closed) against Odoo at create/update time — closing the exact gap this doc's "Recommendation" section flagged as the reason to keep two ids.

# Should `employees` collapse to a single Odoo-sourced id?

## The question

`employees` currently has two ids: an internal `BIGSERIAL PRIMARY KEY id` and a
separate `employee_id VARCHAR(20) NOT NULL UNIQUE` business key sourced from
Odoo (`internal/adapters/postgresql/migrations/00001_create_employees.sql:5-6`).
Every handler, FK, and sync path in this repo currently keys off `id`, with
`employee_id` used only as the join key to Odoo. This note asks: what would it
cost, and what would it buy, to drop `id` and make `employee_id` (or a renamed
Odoo-sourced field) the sole primary key?

This is pre-decision research, not an ADR — nothing here has been implemented
or agreed. It's written to seed a future spec or ADR if someone decides to
act on it.

---

## Facts grounded in code

### The current schema

- `employees.id BIGSERIAL PRIMARY KEY`, `employees.employee_id VARCHAR(20) NOT NULL UNIQUE`
  (`internal/adapters/postgresql/migrations/00001_create_employees.sql:4-6`).
- `password_reset_tokens.employee_id BIGINT NOT NULL REFERENCES employees(id) ON DELETE CASCADE`
  — this FK targets the internal bigserial `id`, not the business key
  (`internal/adapters/postgresql/migrations/00002_create_password_reset_tokens.sql:4`).
- `employees.store_id BIGINT REFERENCES store(id) ON DELETE SET NULL`
  (`internal/adapters/postgresql/migrations/00007_add_employees_store_id.sql:3`).
  This is the reverse direction (employees referencing store's internal id) but
  establishes that `store_id` on this table is already an internal-id FK, not
  an Odoo-id FK.
- The precedent case, `store`: `id BIGSERIAL PRIMARY KEY`, `odoo_store_id VARCHAR(20) UNIQUE`
  — but **nullable**, unlike `employees.employee_id` which is `NOT NULL`
  (`internal/adapters/postgresql/migrations/00006_create_store_geofence.sql:3-4`).
  sqlc reflects this: `repo.Store.OdooStoreID` is `pgtype.Text` (nullable),
  `repo.Employee.EmployeeID` is a plain `string` (not-null)
  (`internal/adapters/postgresql/sqlc/models.go:16`, `:39`).

### How `employee_id` is used end to end

- `odoo.Employee` is keyed by `EmployeeID string`; the type comment states
  Odoo is treated as authoritative for this value and "Odoo never sees our
  internal bigserial id" (`internal/odoo/client.go:14-18`). Note the asymmetry
  with `odoo.Store`, which is keyed by `ID int` (`internal/odoo/client.go:8-9`)
  — Odoo's employee identifier is modeled as an opaque string here, its store
  identifier as an integer. Nothing in this repo explains why, which itself
  is worth flagging: it's unclear whether that's a real difference in Odoo's
  data model or just an artifact of how each integration was written.
- `POST /v1/employees` (`CreateEmployee`) takes `employeeId` as a caller-supplied,
  required string field with no format/uniqueness check against Odoo —
  `createEmployeeParams.EmployeeID string \`validate:"required"\`` (`internal/employees/types.go:39`).
  The handler calls `s.repo.CreateEmployee` directly
  (`internal/employees/handlers.go:35`, `internal/employees/service.go:58-76`);
  nothing in this path calls Odoo or validates the value is a real
  Odoo-recognized id. **This means an employee can be created in this system
  with an `employee_id` Odoo has never heard of, or that doesn't match Odoo's
  real value yet** — the two only get reconciled later, if an admin explicitly
  calls `SyncEmployees`.
- `SyncEmployees` takes **internal `id` values**, not `employee_id` values
  (`syncEmployeesParams.IDs []int64`, `internal/employees/types.go:114-116`,
  comment: "IDs are internal employees.id values ... not Odoo employee_ids").
  The service then: (1) resolves those internal ids to `employee_id`s via
  `ListEmployeeIDsByIDs` (`internal/employees/service.go:264`,
  query at `internal/adapters/postgresql/sqlc/queries.sql:49-54`, silently
  dropping unmatched ids per the query's own comment); (2) pages through those
  `employee_id`s in batches of 50 to `odoo.FetchEmployeesByEmployeeIDs`
  (`internal/employees/service.go:293-307`, `employeeSyncBatchSize = 50` at
  `internal/employees/service.go:43`); (3) bulk-upserts the results back by
  `employee_id` via `ON CONFLICT (employee_id) DO UPDATE`
  (`internal/adapters/postgresql/sqlc/queries.sql:56-70`,
  `internal/employees/service.go:334-340`).
- `UpsertEmployees`'s conflict target is explicitly `employee_id`, not `id`
  (`internal/adapters/postgresql/sqlc/queries.sql:64`, comment at `:60-61`:
  "employee_id is the shared key with Odoo ... so it's the conflict target").
- Every other employee-facing endpoint (`GetEmployeeByID`, `UpdateEmployee`,
  `SetEmployeePassword`, `SetEmployeeActive`, `DeleteEmployee`,
  `BulkDeleteEmployees`, `BulkSendPasswordResetLinks`) is keyed by the internal
  `int64 id`, parsed from the URL path via `strconv.ParseInt`
  (`internal/employees/handlers.go:49`, `69`, `105`, `135`, `165`, and
  `bulkDeleteEmployeesParams.IDs []int64` / `bulkSendPasswordResetLinksParams.IDs []int64`
  at `internal/employees/types.go:78`, `:97`).

### What the store side does when a record disappears from Odoo — and what employees does not have

- `SyncStores` hard-deletes a store once it's absent from Odoo's response:
  `FindStoresNotInOdoo` → `DeleteStores`
  (`internal/stores/service.go:503`; query comment at
  `internal/adapters/postgresql/sqlc/queries.sql:115-121`: "Hard-deletes
  stores Odoo no longer reports (see ADR-0005)"). This is a deliberate,
  documented decision: ADR-0005
  (`docs/adr/0005-store-removal-from-odoo-is-hard-delete.md`) walks through why
  soft-delete was replaced with hard delete, and explicitly calls out the
  accepted risk of a transient Odoo issue causing an unrecoverable delete
  (`docs/adr/0005-store-removal-from-odoo-is-hard-delete.md:19-21`).
- **No equivalent exists for employees.** A repo-wide search for
  `NotInOdoo`/`SoftDelete`/`HardDelete` inside `internal/employees/` returns
  nothing — only `internal/stores/` has this logic and its tests
  (`internal/stores/service.go:503`, `internal/stores/service_test.go:565-762`).
  `SyncEmployees`/`runSync` only fetches and upserts; an employee whose
  `employee_id` Odoo stops recognizing is left untouched in this database,
  merely logged as `notFound`
  (`internal/employees/service.go:296`, `324-328`, `355-356`). There is no
  ADR and no code path that decides what "an employee disappeared from Odoo"
  should mean. This is a genuine, current gap in this codebase, independent of
  the id-model question — but it directly bears on the id-model question,
  because a single-id design's blast radius on deletion/reuse is exactly the
  kind of thing this gap leaves undefined.
- `CONTEXT.md`'s glossary defines **Store**, **Wifi Whitelist**, and
  **Geofence**, but has no **Employee** entry at all
  (`CONTEXT.md:9-19`). The domain vocabulary for how this codebase should
  reason about "an employee" as a concept — including its relationship to
  Odoo — isn't written down anywhere yet.

### No live Odoo integration exists to validate assumptions against

- `internal/odoo/fake_client.go:30-33` states outright: "a deterministic,
  in-memory stand-in for a real Odoo connection (Phase 2 of the store-sync
  spec: no live Odoo integration exists yet)." The fake's employee ids are
  synthetic, sequential, and fixed-format (`"ODOO-EMP-%03d"`,
  `internal/odoo/fake_client.go:26-28`). Nothing in this repo demonstrates
  what a real Odoo `employee_id` looks like, whether it's ever reused,
  reformatted, or reassigned after an employee record is deleted and
  recreated in Odoo.

---

## Mechanical impact of collapsing to a single id

If `employees.id` is dropped and `employee_id` becomes the sole PK:

1. **`password_reset_tokens.employee_id` FK** (`internal/adapters/postgresql/migrations/00002_create_password_reset_tokens.sql:4`)
   changes type from `BIGINT` to `VARCHAR(20)` (or whatever the new PK type is)
   and re-targets `employees(employee_id)`. Every write/read through this
   table changes: `repo.PasswordResetToken.EmployeeID` (currently `int64`,
   `internal/adapters/postgresql/sqlc/models.go:30`) becomes a string;
   `CreatePasswordResetToken`/`RedeemPasswordResetToken` call sites in
   `internal/employees/service.go:189-193, 412-425` pass `employee.ID` today —
   they'd need to pass `employee.EmployeeID` (or whatever the merged field is
   named) instead.
2. **Every handler that parses a URL-path id with `strconv.ParseInt`**
   (`internal/employees/handlers.go:49, 69, 105, 135, 165`) would need to
   switch to accepting a string path param instead — a real routing/API-shape
   change, not just an internal type swap, since the `id` in the URL is
   client-visible (`GET /v1/employees/{id}`, etc.).
3. **Every `[]int64` id-batch field** in bulk-operation request bodies —
   `bulkDeleteEmployeesParams.IDs`, `bulkSendPasswordResetLinksParams.IDs`,
   `syncEmployeesParams.IDs` (`internal/employees/types.go:78, 97, 114-116`) —
   becomes `[]string`. `BulkActionResult.ID` (`internal/employees/types.go:86`,
   currently `int64`) becomes a string too, which is a response-shape change
   for existing frontend consumers.
4. **`SyncEmployees`'s indirection collapses.** Today it's: internal id →
   `ListEmployeeIDsByIDs` lookup → `employee_id` → Odoo → upsert by
   `employee_id` (`internal/employees/service.go:259-276`, query at
   `internal/adapters/postgresql/sqlc/queries.sql:49-54`). With a single id,
   step 1 (the lookup query) is unnecessary — callers would pass `employee_id`
   values directly, since that's now the same as the row's identity. This is
   a real simplification and the clearest "for" argument grounded in the code:
   `ListEmployeeIDsByIDs` and its query and mock expectations
   (`internal/employees/service_test.go:1467-1470, 1528, 1556-1568`) go away
   entirely.
5. **sqlc-generated code regenerates wholesale**: `repo.Employee.ID` field
   type change ripples through every generated query in
   `internal/adapters/postgresql/sqlc/queries.sql.go` that touches `employees`
   or joins to it (`CreateEmployee`, `GetEmployeeByID` [renamed/retyped],
   `UpdateEmployee`, `SetEmployeePassword`, `SetEmployeeActive`,
   `DeleteEmployee`, `UpsertEmployees`'s `RETURNING id`
   at `internal/adapters/postgresql/sqlc/queries.sql:70`, and both
   `password_reset_tokens` queries).
6. **Tests**: every mock/table-driven test in
   `internal/employees/service_test.go` and `internal/employees/handlers_test.go`
   that constructs `repo.Employee{ID: ...}`, calls service methods with an
   `int64` id, or asserts on `ListEmployeeIDsByIDs` mock expectations breaks
   and needs rewriting — this is a broad, mechanical, not-conceptually-hard
   diff, but it touches most of the test file (the sync tests alone span
   `internal/employees/service_test.go:1383-1568`).
7. **`employeeResponse.ID`** (`internal/employees/types.go:132`, currently
   `int64`) becomes a string in the JSON API — an external contract change for
   any frontend or API consumer.

## Argument for the single-id design

Grounded in what's actually observed:

- It removes one real indirection: the internal-id → `employee_id` lookup
  step in `SyncEmployees` (`internal/employees/service.go:264`,
  `ListEmployeeIDsByIDs`) exists *only* because the two ids are different.
  Collapse them and that query, its mock, and its "id with no matching row is
  silently dropped" comment (`internal/adapters/postgresql/sqlc/queries.sql:49-52`)
  all disappear — one less lookup, one less place an id can silently vanish
  between "what the caller sent" and "what actually got synced."
- It removes the possibility of the two ids drifting apart from a coding
  mistake — e.g. a future call site accidentally passing `employee.ID` where
  `employee.EmployeeID` was meant, or vice versa. Nothing in the current code
  shows this has happened, but the dual-id shape is exactly the shape where
  it *could* (two same-typed-looking int/string identifiers on the same
  struct, used in different contexts).
- One id is a smaller mental model for anyone reading this package for the
  first time — there's currently a real asymmetry to track (URLs/bulk-ops use
  `id`; Odoo/sync uses `employee_id`) that a single id removes by
  construction.

## Argument for keeping two ids (the current design)

Grounded in what's actually observed:

- **`POST /v1/employees` does not require an Odoo-recognized `employee_id`.**
  `createEmployeeParams.EmployeeID` is caller-supplied and merely
  `validate:"required"` (`internal/employees/types.go:39`) — there is no call
  to `odoo.Client` anywhere in `CreateEmployee`
  (`internal/employees/service.go:58-76`). This means, today, an admin can
  create an employee row before that employee exists in Odoo at all, or with
  an `employee_id` value that later turns out not to match Odoo's real one
  (reconciled, if ever, only by an explicit `SyncEmployees` call). A
  single-id design built around "the PK is the Odoo id" would either have to
  (a) forbid creating an employee without first confirming the id against
  Odoo — a real behavior change to `POST /v1/employees` — or (b) accept that
  the "Odoo id" PK can itself be provisional/unverified data, which weakens
  the "it's the authoritative business key" premise the single-id design
  rests on.
- **FK stability under a value that isn't validated at write time.** Because
  `employee_id` is caller-supplied free text at creation and only ever
  format-constrained by `VARCHAR(20) NOT NULL UNIQUE`
  (`internal/adapters/postgresql/migrations/00001_create_employees.sql:6`),
  making it the PK means every dependent FK (`password_reset_tokens`, and any
  future employee-referencing table) inherits whatever churn that field is
  subject to. Today, with `id` as the stable PK, `employee_id` can be
  corrected via `UpdateEmployee` (`internal/employees/service.go:82-105`,
  query at `internal/adapters/postgresql/sqlc/queries.sql:22-31`, which sets
  `employee_id = $2` alongside other fields) without needing to cascade a PK
  change through `password_reset_tokens`. If `employee_id` were the PK, the
  same correction becomes a PK update — cascading (with `ON UPDATE CASCADE`,
  not currently used anywhere in this schema) or blocked, depending on how
  it's built.
- **The `store` precedent in this same codebase deliberately keeps the two ids
  separate, and additionally makes the Odoo-sourced one nullable**
  (`internal/adapters/postgresql/migrations/00006_create_store_geofence.sql:3-4`).
  That nullability is only possible because `store.id` is independent of
  `odoo_store_id` — a store row can exist with no Odoo linkage at all. Whether
  employees currently need that same allowance isn't demonstrated in code
  (`employees.employee_id` is `NOT NULL`), but the `CreateEmployee` path above
  shows the *practical* effect is similar: an employee can exist in this
  database in a state not yet confirmed against Odoo, even though the column
  itself is non-null.
- **No employee-disappears-from-Odoo policy exists yet**
  (see Facts section above — no `NotInOdoo` equivalent, no ADR). Under the
  current two-id design, that gap is low-stakes: nothing keyed to Odoo's
  employee identifier stability is load-bearing for FK integrity, because the
  FK graph (`password_reset_tokens`) points at the internal `id`, which never
  changes regardless of what Odoo reports. Under a single-id design, that same
  gap becomes higher-stakes: if a future policy ever needed to treat an
  Odoo-vanished employee the way ADR-5 treats a vanished store (hard delete),
  a hard delete keyed on the PK itself removes the row and cascades through
  every FK immediately — there's no internal id left to preserve reset-token
  history or other references under. Today `ON DELETE SET NULL` on
  `employees.store_id` softens exactly this kind of cascade risk on the store
  side (`internal/adapters/postgresql/migrations/00009_employees_store_id_set_null_on_delete.sql:3-5`);
  the same softening is harder to design for the employee row's own identity
  once that identity is Odoo's.

## Risks specific to this codebase's current unknowns

- **`employee_id` stability/reuse/format is unvalidated against a real Odoo
  instance.** Everything this repo currently knows about it comes from
  `fake_client.go`'s synthetic `"ODOO-EMP-%03d"` format
  (`internal/odoo/fake_client.go:26-28`) — there is no live integration
  (`internal/odoo/fake_client.go:30-33`). A single-id design implicitly bets
  that Odoo's real employee identifier is permanent, never reused after
  deletion, and never reformatted. None of that is confirmed anywhere in this
  codebase. If any of it turns out false once real Odoo integration lands,
  every FK built directly on that value (not just `password_reset_tokens` —
  any future employee-referencing table) inherits the blast radius.
- **The `EmployeeID string` vs. `Store.ID int` type asymmetry in
  `internal/odoo/client.go:8-18`** is unexplained in this repo. If it reflects
  a real difference in how Odoo exposes the two record types (e.g. employee
  ids are alphanumeric codes, store ids are simple sequence numbers), that's
  relevant to whether `employee_id` is a safe, compact PK type — but nothing
  here confirms or denies it either way.
- **No defined behavior for what happens if `employee_id` needs to change**
  (a typo at creation time, an Odoo-side renumbering, a merge of duplicate
  Odoo records). Today `UpdateEmployee` handles this trivially because it's
  updating a non-PK unique column. Under a single-id design this becomes a PK
  update, which is a meaningfully different (and riskier) class of operation,
  and no code path or ADR in this repo currently anticipates it.

## Recommendation (judgment, not a fact from the codebase)

Keep the two-id design. The strongest concrete reason isn't abstract
database-design caution — it's that `POST /v1/employees`
(`internal/employees/types.go:39`, `internal/employees/service.go:58-76`)
already proves this codebase treats `employee_id` as caller-supplied,
unverified-at-write-time data, not a confirmed Odoo business key at the
moment a row is created. A PK is expected to be a value the system already
trusts by the time it's assigned; `employee_id` here is exactly the value
that's *most* likely to need correcting after the fact (via `UpdateEmployee`,
which already exists and is exercised) or to exist temporarily out of sync
with Odoo. Building the PK — and every FK a future feature adds — directly on
that value trades away a correction path this codebase currently uses for a
simplification (removing `ListEmployeeIDsByIDs`) that's real but narrow: it
only pays off inside `SyncEmployees`, one code path out of the whole package.

The `store` precedent is a supporting data point but not the deciding one:
`store` keeps the two ids *and* makes the Odoo-sourced one nullable
(`internal/adapters/postgresql/migrations/00006_create_store_geofence.sql:3-4`),
which is a stronger version of the same argument (a store can exist with no
Odoo linkage at all) — consistency with that precedent favors keeping two ids
here too, but the `employees` case for keeping them stands on its own
evidence (the `CreateEmployee` path) independent of matching `store`.

Before revisiting this, the highest-value next step is closing the gap noted
above, not the id-model question itself: there is currently no
employee-disappears-from-Odoo policy anywhere in this repo (no ADR, no
`NotInOdoo`-style query, no `CONTEXT.md` entry for "Employee" at all). ADR-5's
reasoning about stale-Odoo-data risk
(`docs/adr/0005-store-removal-from-odoo-is-hard-delete.md:19-21`) would need
an employee-side counterpart before anyone could responsibly reason about
what a single, Odoo-owned employee id would need to survive.

---

*Filed under `.scratch/employee-id-model/` per this repo's issue-tracker
convention (`docs/agents/issue-tracker.md`) — feature-slug directories are
meant for specs/planned work, and this is neither an accepted decision
(`docs/adr/`) nor an implementation spec yet. This is a judgment call on
where to put not-yet-decided research; redirect to `docs/adr/` (as a
"proposed" status ADR) or a plain `docs/` note if you'd prefer either of
those instead.*
