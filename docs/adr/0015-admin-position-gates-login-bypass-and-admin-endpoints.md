---
status: accepted
---

# The "Admin" Position gates both Login's presence check and every admin endpoint, bootstrapped by a seed script

Every admin endpoint in this codebase (Employees, Stores, Positions CRUD) has been deliberately unauthenticated since the Odoo-integration feature explicitly scoped auth out (`docs/flows/admin-create-employee.md`). Building Login/Session ([ADR-0014](/docs/adr/0014-session-lifecycle-opaque-tokens-heartbeat-logout.md)) closes that gap — but there is no dedicated authorization concept in the schema to gate it with, only Position: a free-form, admin-managed job title with no fixed vocabulary (see `CONTEXT.md`). A proper Grant/Assign Permission feature is planned, but not yet built.

## Decision

An Employee holding the Position named **"Admin"** (exact match, case-insensitive, checked through one centralized function rather than duplicated per call site) gets two effects:

1. **Login bypass** — skips the IP → Geofence → MAC presence check entirely (see ADR-0013).
2. **Admin-endpoint access** — their Session is required to call every existing admin endpoint (Employees, Stores, Positions), which now reject requests without a valid Admin Session.

"Super Admin" is explicitly not a distinct concept anywhere in Login or Session — any finer-grained distinction between admin levels is deferred entirely to the future Grant/Assign Permission feature.

**Bootstrap**: creating an Employee now requires an authenticated Admin Session, but no Admin Session can exist before an Admin Employee does. The first Admin Employee is created by a one-off script writing directly to Postgres — bypassing both the HTTP API and the existing Odoo-employee-id existence check ([ADR-0007](/docs/adr/0007-employee-odoo-id-is-validated-not-owned.md)) — run once per environment, not a permanent code path.

## Considered Options

- **A dedicated `employees.is_admin` boolean** — the more durable choice, rejected only because a real permission system is already planned; a boolean would still need replacing once that ships, so the extra migration wasn't judged worth it for an interim mechanism.
- **An API/env-var bootstrap path** ("allow the first admin creation if the table is empty") — rejected as a permanent latent hole in the API kept around for a one-time need. A script run outside the API leaves no such code path behind.

## Consequences

- Renaming or deleting the "Admin" Position (both already possible through the existing Position endpoints) silently changes who bypasses Login's presence check and who can reach admin endpoints — there is no guardrail against this. Accepted as a known, temporary gap, to be closed when Position-name matching is replaced by real Grant/Assign Permission.
- Every existing admin endpoint, previously reachable by anyone, now requires an Admin Session — any external caller (scripts, other services) integrating against these endpoints today breaks until it authenticates.
