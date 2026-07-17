Status: ready-for-agent

# Employee/Odoo integration: real client, id rename, Position, multi-store

## Problem Statement

Employee records in this system aren't actually trustworthy against Odoo, the
company's ERP and source of truth for who works here. Today, an admin can
create an employee with any string as its `employee_id` — nothing checks it
against Odoo, so it can be a typo, a placeholder, or simply wrong, and it's
only ever reconciled later if someone happens to run a sync. All Odoo
employee and store data currently comes from an in-memory fake, not the real
`erp.bangnails.fr` instance, so there's no live integration to trust in the
first place.

Two more mismatches compound this: an employee can only ever be assigned one
`role` (a free-text string) even though staff genuinely cover more than one
job, and can only ever belong to one store even though staff genuinely work
across multiple salon locations — both realities Odoo's own data model
already reflects (`x_pos_shop_ids` is a multi-valued field), but this
system's schema can't represent.

## Solution

Replace the fake Odoo integration with a real one (OAuth2 password grant
against Odoo's MuK REST API), and use it to validate an employee's Odoo
identity at creation and update time rather than trusting caller input.
Replace the single free-text `role` with a locally-managed, many-to-many
`Position` concept. Replace single-store assignment with many-to-many store
membership, kept in sync one-way from Odoo. The two-id design (internal
primary key + Odoo-sourced business key) is kept, not collapsed — see
ADR-0007.

## User Stories

1. As an admin, I want to create an employee record that references a real Odoo employee id, so that our employee data stays trustworthy and linked to the ERP.
2. As an admin, I want employee creation to fail clearly if the Odoo id doesn't exist, so that I don't accidentally onboard someone with a typo'd or fake id.
3. As an admin, I want employee creation to fail if Odoo can't be reached at all, so that I never create an employee record whose Odoo id was never actually verified.
4. As an admin, I want to change an employee's Odoo id and have it re-validated against Odoo, so that corrections stay trustworthy.
5. As an admin, I want an update that doesn't touch the Odoo id to skip the Odoo check entirely, so that routine edits (name, positions) aren't slowed down or blocked by an unrelated Odoo outage.
6. As an admin, I want to assign more than one position to an employee, so that staff who cover multiple roles (e.g. technician and cashier) are represented accurately.
7. As an admin, I want to manage the list of positions myself (create, rename, delete), so that our internal job-title vocabulary doesn't depend on Odoo.
8. As an admin, I want to delete a position that's currently assigned to employees without being blocked, so that cleaning up unused/renamed titles is simple — those employees just lose that one assignment.
9. As an admin, I want an employee to be able to exist with zero positions, so that a newly created employee isn't forced to have a title before I've decided one.
10. As an admin, I want to replace an employee's entire set of positions in one call, so that reassigning someone's job titles is a single, simple operation rather than one-at-a-time edits.
11. As an admin, I want to see which stores an employee is assigned to, reflecting Odoo's own multi-store field, so that I know where staff actually work without maintaining that by hand.
12. As an admin, I want employee-store assignments to update automatically whenever I run an employee sync, so that I never have to manually reassign staff to stores as Odoo changes.
13. As an admin, I want an employee's store assignments to be removed automatically when a store is deleted, so that stale references never linger.
14. As an admin, I want an employee's position assignments to be removed automatically when I delete a position, so that deleting a position never fails just because someone happened to hold it.
15. As an admin, I want an employee's name and email to stay in sync with Odoo over time, so that our records don't silently drift from the ERP.
16. As an admin, I want usernames to stay entirely under my control, unaffected by any Odoo sync, so that login credentials are never changed by an unrelated system.
17. As a developer maintaining this backend, I want the Odoo integration to use the documented MuK REST API instead of an in-memory fake, so that this system reflects real Odoo data, not synthetic placeholders.
18. As a developer, I want Odoo credentials (OAuth2 client id/secret, service username/password) stored as environment variables, so that they're never hardcoded and follow this repo's existing secrets convention.
19. As a developer, I want the Odoo client to transparently re-authenticate when its access token expires, so that long-running sync jobs don't fail on token expiry.
20. As a developer, I want the existing two-id design (internal primary key, separate Odoo-sourced business key) preserved rather than collapsed into one id, so existing foreign keys and URL/bulk-operation shapes don't have to change.
21. As a developer, I want the Odoo-sourced business key renamed and retyped to match what it actually is (an Odoo integer id, not an arbitrary string), so the schema stops implying a shape Odoo doesn't actually use.
22. As a developer, I want the employee-store sync to gracefully skip a store id it can't yet resolve (rather than failing the whole run), so a not-yet-synced store doesn't block unrelated employees from syncing correctly.

## Implementation Decisions

**Employee identity**
- The employee table's Odoo-sourced business key is renamed and retyped: from a free-text, caller-supplied string to a required, unique integer sourced from and validated against Odoo. The internal primary key is untouched — every existing handler, foreign key, and URL keeps keying off it.
- No backfill migration is needed (no production employee data exists yet); this is a destructive schema change.
- `CreateEmployee` calls Odoo to confirm the submitted Odoo id exists. This is existence-only — a match lets the write through with the admin-submitted name/email as-is; Odoo's response is never used to overwrite them. No match, or the Odoo call itself failing (timeout, 5xx, network error), rejects the write (fail closed).
- `UpdateEmployee` performs the same existence check, but only when the Odoo id is actually part of the update and differs from the current value — updates that don't touch it never call Odoo.

**Position** (replaces the old single free-text role)
- A new, admin-managed lookup concept: a unique name, nothing else. Full CRUD exposed via the API.
- Never sourced from or synced with Odoo — purely local.
- The relationship between an employee and its positions is many-to-many: an employee can have zero, one, or many positions at once.
- Deleting a position that's currently assigned to employees is allowed — it simply removes those assignments, it does not block the delete or delete the employees.
- Assigning positions to an employee (on create or update) is a whole-set replace: the caller submits the full desired set, and the system diffs it against the current set (insert newly added, remove no-longer-present, leave unchanged) rather than requiring one-at-a-time add/remove calls.
- Any submitted position must reference a real position; an unknown one is a client error, not a raw database constraint failure.

**Store membership** (replaces the old single store assignment)
- The relationship between an employee and stores becomes many-to-many, matching Odoo's own multi-valued field for this — an employee can belong to zero, one, or many stores.
- Unlike Position, this relationship is Odoo-owned: it's populated exclusively by the existing employee-sync job, never directly editable by an admin.
- Employee sync resolves each Odoo-side store identifier to this system's internal store record (via the same Odoo-store join key the existing store sync already uses), then diffs the employee's stored assignments against that resolved set (insert new, remove stale) — the same pattern already used for Wifi Whitelist list replacement.
- A store identifier from Odoo that doesn't (yet) resolve to a known internal store record is logged and skipped for that one assignment — it does not fail the rest of that employee's sync, or any other employee's. This mirrors the existing precedent for an employee id Odoo no longer recognizes.
- Deleting a store removes any employee's membership for it automatically (cascading), rather than needing a separate cleanup step.

**Sync field ownership**
- Odoo remains the one-way source of truth, kept current by the existing sync job, only for: name, email, and store membership.
- Username and Position are local-only: set once at creation, changed only through this system's own update path, never touched by sync — this is a change from today, where the sync job also currently overwrites username and role.

**Real Odoo client**
- Replaces the current in-memory fake entirely, including its synthetic data — real network calls only.
- Authentication: OAuth2 password grant against Odoo's own token endpoint. Client credentials (client id/secret) are sent via the HTTP `Authorization` header; the service account's username/password are sent in the request body. The access token is cached in memory for reuse; when it expires (or a call comes back unauthorized), the client transparently repeats the password-grant request rather than requiring a separate refresh-token exchange (Odoo's actual token response hasn't been confirmed to include one, and re-authenticating this way always works regardless).
- Data queries against Odoo use its `search_read`-style query capability: a domain filter, a field list, and pagination — used both for the single-employee existence check and for batched employee lookups during sync.
- All Odoo connection details (base URL, service account credentials, OAuth client id/secret) are configured via environment variables, following this repo's existing convention for secrets.

## Testing Decisions

Tests should exercise externally observable behavior — request/response shapes, database state, and calls to collaborators — not internal implementation details. Three seams, matching this repo's existing testing patterns as closely as possible (fewest new seams, highest possible in each case):

1. **Employee service layer** — exercised through its existing public methods (create, update, sync) with a mocked data-repository and a mocked Odoo client, exactly the seam this repo's employee service tests already use today. Covers: existence validation on create/update and its fail-closed behavior, the "only re-validate when the Odoo id changes" rule, position-set diffing, and the extended sync behavior (store-membership diffing, and no longer overwriting username/position).
2. **Position service layer** — a new service, but tested the exact same way every other service in this repo already is: its public methods, with a mocked data-repository. Covers: CRUD behavior and that deleting an in-use position doesn't fail or affect the employees that held it.
3. **Real Odoo client** — the one genuinely new seam, because it's the actual network boundary. Tested against a local test HTTP server standing in for Odoo, asserting the client sends the correct authentication request and data-query requests, correctly caches and re-uses its access token, and correctly re-authenticates on expiry/unauthorized responses — rather than mocking `net/http` internals directly.

Handler-layer tests (both for employees and the new positions endpoints) stay on this repo's existing pattern: exercised through the HTTP handler with a mocked service, asserting request validation and response/status-code mapping — unchanged in kind from what already exists.

## Out of Scope

- No authentication/authorization gating is added to any endpoint (employees, positions, or otherwise) — this matches the existing, repo-wide state today and is not part of this change.
- No dedicated single-item add/remove endpoints for an employee's positions or stores — both are whole-set-replace only, for now.
- No OAuth2 refresh-token flow — re-authentication always goes through the password grant again.
- No backfill migration for existing employee data — none exists yet to backfill.
- No changes to Wifi Whitelist or Geofence login-check logic.
- Position is never synced to or from Odoo, in either direction.

## Further Notes

- This closes the gap identified by an earlier research note on the employee id model (kept as historical record, now marked resolved): the two-id design is kept exactly as that note recommended, and the reason it gave for keeping it — that the Odoo-sourced id wasn't validated at write time — no longer applies once this ships.
- Three ADRs record the reasoning behind the harder-to-reverse calls here: the Odoo id being validated-not-owned, Position's many-to-many/local-only shape, and store membership's many-to-many/Odoo-owned shape. An existing ADR about store deletion behavior is updated with a pointer noting one of its consequences (nulling an employee's store column) is superseded now that column no longer exists.
- The project's domain glossary has been updated with `Employee` and `Position` entries, and a revision to `Store`'s entry reflecting the new cascade behavior.
