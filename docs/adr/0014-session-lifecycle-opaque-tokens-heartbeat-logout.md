---
status: accepted
---

# Sessions are opaque Postgres tokens, one per Employee, ended by a server-verified Heartbeat

There is no session/auth mechanism anywhere in this codebase today — the closest existing token model, `password_reset_tokens`, is redeemed once and discarded, not a live session. Login needs one, and it needs a specific property JWTs are bad at: a Session must be killable *instantly*, the moment Heartbeat detects the Employee has left (see [ADR-0013](/docs/adr/0013-login-verification-order-ip-geofence-mac.md)). A self-contained signed token can't do that without an equivalent server-side revocation check on every request anyway — at which point the signature verification is pure overhead on top of a lookup you needed regardless.

## Decision

- **Opaque tokens, not JWTs.** A new Postgres `sessions` table stores `token_hash` (never the raw token — same convention as `password_reset_tokens`), so a DB leak/backup/replica can't be replayed as a live session. No Redis: at ~200 Employees, even a 30-second Heartbeat cadence per active Employee is a handful of requests per second, well within a single indexed Postgres lookup — a second datastore isn't justified at this scale.
- **One active Session per Employee.** A new Login invalidates any Session already open for that Employee.
- **Non-Admin Sessions** carry the Store their Login matched (see ADR-0013) and are Heartbeat-monitored (below). They expire after **12 hours** regardless of Heartbeat outcome, as a backstop against a forgotten Logout.
- **Admin Sessions** carry no Store and are never Heartbeat-monitored — there is no presence check to re-verify, since Admin Login skips it entirely (see [ADR-0015](/docs/adr/0015-admin-position-gates-login-bypass-and-admin-endpoints.md)). They expire after a flat **8 hours**.
- **Heartbeat**: while a non-Admin Session is open, the app polls a heartbeat endpoint roughly every **30 seconds**; the server reruns Login's IP → Geofence → MAC check against the Session's Store. The server decides pass/fail — the app is never trusted to self-report "I've disconnected." **Two consecutive failures** end the Session. If no heartbeat (success or failure) arrives for **90 seconds** (3x the interval), the Session also expires — the backstop for a device that can't call out at all (killed app, dead battery, fully offline). Every forced Session-end carries a reason code (e.g. left the premises, expired, superseded by a newer Login) so the app can tell the Employee why, instead of a bare "please log in again."
- **Voluntary Logout** is a separate, simpler action: the Employee ends their own Session on demand, no re-authentication required.
- **Sessions are not an attendance record.** A separate clock-in/clock-out feature owns hours-worked history; the `sessions` table only needs to represent currently-open and recently-ended sessions, not a permanent audit trail.

## Considered Options

- **JWT + revocation denylist** — rejected: once a denylist lookup is required on every request anyway (to support instant Heartbeat-triggered revocation), a JWT's signature verification adds cost without removing the lookup it was supposed to make unnecessary.
- **Redis-backed sessions** — rejected at current scale (~200 Employees, single API instance). Revisit if request volume or multi-instance fan-out ever makes a single Postgres lookup insufficient.

## Consequences

- Every authenticated request (non-Admin) needs a `sessions` row lookup — this is the point, not an incidental cost.
- The app must implement a background heartbeat loop, and must treat a heartbeat response telling it to discard its Session (reason code) as a first-class case, not an error path.
- Killing an Employee's Session in response to a lost device or security incident is a single row update — no token-expiry waiting period.
