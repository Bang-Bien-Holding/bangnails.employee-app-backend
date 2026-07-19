Status: resolved

# Store sync: async trigger + status poll, mirroring Employee Sync

## Problem Statement

Today, `POST /stores/syncs` runs synchronously inside the HTTP request: it
blocks until every store is fetched from Odoo, upserted, and any store Odoo
no longer reports is hard-deleted, returning a result summary only once all
of that has finished. Employee Sync works differently — it returns
immediately (202 Accepted) and lets the caller poll a separate status
endpoint for completion. This asymmetry forces the frontend into two
different interaction patterns for what should be the same "trigger a sync,
watch it finish" workflow: one entity's sync ties up the request for however
long a full Odoo fetch and database write takes, the other doesn't.

## Solution

Convert Store Sync to the same two-endpoint, fire-and-poll shape Employee
Sync already uses: `POST /stores/syncs` kicks off a background job and
returns immediately; a new `GET /stores/syncs` reports whether that job is
still running. Store Sync keeps its existing full-scope behavior — it always
syncs every store Odoo reports, with no id-based selection (unlike Employee
Sync, which targets specific admin-chosen ids). This spec changes only how
the caller is told about progress and completion, not what gets synced.

## User Stories

1. As an admin, I want triggering a store sync to return immediately, so that I'm not stuck waiting on a blocking request while Odoo is fetched and every store is upserted.
2. As an admin, I want to poll whether a store sync is still running, so that I can keep a "Sync" button disabled or show a spinner until it completes, the same way I already can for employee sync.
3. As an admin, I want store sync to always cover every store Odoo reports, with no way to select a subset, so that I never accidentally leave a store out of sync.
4. As an admin, I want a store sync I trigger while one is already running to be rejected clearly, so that I know a previous sync is still in progress rather than silently starting a second, conflicting one.
5. As an admin, I want the store sync's insert/update/delete counts still recorded somewhere an engineer can find them, so that unusual sync outcomes (e.g. a store disappearing) aren't invisible even though the API no longer returns them directly.
6. As a developer, I want the store-sync background job bounded by a timeout, so that a stalled Odoo or database call can't leave the "sync in progress" flag stuck forever.
7. As a developer, I want the store-sync trigger and status endpoints to mirror employee sync's response shapes exactly, so the two features stay consistent and predictable for anyone building against this API.
8. As a developer, I want the existing fetch-all/upsert/hard-delete-stale transaction logic left untouched, so that this change only affects how progress is reported, not what sync actually does.
9. As a developer, I want the store sync's concurrency guard (only one run at a time) preserved exactly as it works today, so that behavior doesn't regress during this refactor.
10. As a developer, I want the now-unused SyncSummary response type removed rather than left dangling, so the codebase doesn't carry dead API surface.
11. As a developer, I want the store-sync tests rewritten to exercise the background job the same way employee sync's tests already do (channel-based synchronization, not sleeps), so test style stays consistent across both packages.
12. As a frontend engineer, I want store sync's trigger/status endpoints to return the same field names and status codes as employee sync's, so I can reuse the same polling component for both.
13. As a developer, I want this to be a clean breaking change to `POST /stores/syncs`'s response contract, so that I don't have to maintain two response shapes for the same endpoint — no existing consumer depends on the current synchronous shape.

## Implementation Decisions

**Store Sync trigger (`POST /stores/syncs`)**
- Service method signature changes from `SyncStores(ctx) (SyncSummary, error)` to `SyncStores(ctx) error`, matching `employees.SyncEmployees(ctx, ids) error`'s shape (minus the `ids` parameter — Store Sync takes none).
- Once the concurrency guard's `tryLock` succeeds, the trigger does no synchronous work before returning — it immediately spawns a detached goroutine and returns `nil`. This differs slightly from Employee Sync, which does one synchronous repo lookup (resolving ids to `odoo_employee_id`) before spawning its goroutine; Store Sync has no such per-request lookup since it always operates on every store, so there's no failure path between acquiring the lock and starting the goroutine.
- The handler response becomes `202 Accepted` with `{"status": "accepted", "message": "Store sync started."}`, mirroring `syncEmployeesResponse` exactly. `ErrSyncInProgress` still maps to `409 Conflict`.

**Store Sync status (`GET /stores/syncs`, new)**
- New `SyncStatus(ctx) SyncStatus` service method and handler, structurally identical to `employees.SyncStatus`: `{"syncing": bool}`, reading the same `mu`/`syncing` fields the existing concurrency guard already maintains.

**Background job (runSync)**
- The existing fetch-all-from-Odoo → transactional upsert → hard-delete-stale-stores logic is unchanged in substance, just moved out of the request-response path and into a `runSync` method run in a detached goroutine (`context.WithoutCancel`), bounded by a new `storeSyncTimeout = 5 * time.Minute` constant — the same duration and detachment pattern as `employeeSyncTimeout`.
- On completion, `runSync` logs the outcome via `slog.Info` (total processed, inserted, updated, deleted counts) instead of returning it to a caller — mirroring how `employees.runSync` logs rather than returns its summary. `s.unlock()` moves from the trigger method into `runSync`'s `defer`, since the trigger no longer does the work itself.

**Type/API cleanup**
- `SyncSummary` is deleted from `stores/types.go` (confirmed unused outside the `stores` package) along with the trigger response's `Meta` field.
- The `Service` interface's `SyncStores` signature updates to `error`-only; a new `SyncStatus(ctx) SyncStatus` method is added, with a new `SyncStatus struct { Syncing bool }` type — naming and JSON shape identical to `employees.SyncStatus`.
- Route: `GET /stores/syncs` is added alongside the existing `POST /stores/syncs` in `cmd/api.go`, matching the `/employees/syncs` GET/POST split on the same path.

**Compatibility**
- This is an intentional breaking change to `POST /stores/syncs`'s response contract (201 + summary → 202, no summary). Confirmed no frontend or other consumer currently depends on the synchronous shape, so no coordinated rollout is needed.

## Testing Decisions

Tests should exercise externally observable behavior — request/response
shapes, database state via the mocked repo, and calls to the mocked Odoo
client — not internal implementation details. No new seams are introduced;
both are already used by the `stores` and `employees` packages today:

1. **Stores service layer** (`stores/service_test.go`) — mocked `repo.Querier` via the existing `withTx` stub, and mocked `odoo.Client`. Tests exercise `SyncStores` and `SyncStatus` through their public methods, following the exact channel-based synchronization pattern `employees/service_test.go`'s `TestEmployeeService_SyncEmployees` already established (a mock call closes a `started`/`done` channel; the test `select`s on it against a timeout) rather than sleeping — needed because these tests must observe the effects of a detached goroutine without a synchronous return path.
2. **Stores handler layer** (`stores/handlers_test.go`) — mocked `Service`, asserting the new 202 `{status, message}` response and 409 `ErrSyncInProgress` mapping for `SyncStores`, and the `{syncing: bool}` body for `SyncStatus` — the same mocked-service pattern already used by every other stores handler test.

The existing `SyncSummary`-based tests and the generated `MockService.SyncStores` (in `service_mock_test.go`) are rewritten to match the new signatures, regenerated via the package's existing `mockgen` `go:generate` directive rather than hand-edited. Existing tests for the transactional upsert/hard-delete-stale logic itself (via the mocked `repo.Querier`) carry over unchanged in what they assert — only the surrounding trigger/return-value plumbing changes.

## Out of Scope

- Store Sync's scope stays full-sync-only — no `ids` parameter is added, and this spec doesn't change what triggers a sync to include or exclude a store.
- No change to the underlying fetch-all/upsert/hard-delete-stale transaction logic, its Odoo query shape, or the `odoo.Client` interface.
- No new way to retrieve a past sync's result counts via the API (e.g. no `last_result` field on the status endpoint) — those are log-only, per the confirmed decision to match Employee Sync's shape exactly.
- No authentication/authorization changes.
- No change to Employee Sync itself.

## Further Notes

- This grew out of a review of Employee Sync's already-shipped async pattern. Whether Store Sync's "always full sync, no id selection" scope (kept as-is here) reflects a deliberate business rule or just an accidental difference from Employee Sync's id-scoped design is still an open domain question, raised separately during a `CONTEXT.md` review — it doesn't block this spec, since that scope isn't changing here.
- No ADR is added for the sync/async change itself: it's easily reversible, isn't a surprising architectural choice, and doesn't reflect a new trade-off — it's adopting a pattern this same codebase already established for Employee Sync.
