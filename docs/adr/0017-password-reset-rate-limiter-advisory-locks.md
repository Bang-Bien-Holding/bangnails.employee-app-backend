---
status: accepted
---

# Password-reset rate limiter's TOCTOU race is closed with Postgres advisory locks, not SERIALIZABLE + retry

[ADR-0016](/docs/adr/0016-password-reset-token-invalidation-rate-limit-anti-enumeration.md) shipped `allowPasswordResetRequest` (`internal/employees/ratelimit.go`) as four unguarded round trips — cleanup, count-by-email, count-by-IP, insert — and knowingly left a concurrency gap: a true concurrent burst for the same email or IP could read the same pre-insert count more than once, letting more requests through than the configured 3/email or 10/IP limit. That ADR judged the gap acceptable at the time (a rate limiter's job is to deter, not to guarantee an exact count) and tracked closing it as issue #42. This ADR is the record of closing it.

## Decision

### Two transaction-scoped advisory locks, not SERIALIZABLE isolation

`allowPasswordResetRequest` now runs its whole cleanup/count/insert sequence inside `s.withTx` (the existing helper added for `CompleteActivation` in 319f65a, `pool.Begin(ctx)` at Postgres' default READ COMMITTED). Before the cleanup delete, it takes two `pg_advisory_xact_lock` calls — one keyed on the lowercased email, one keyed on the client IP — via a new `LockPasswordResetRequestKey` query. Both locks are transaction-scoped, so they release automatically on commit or rollback; nothing explicitly unlocks them.

READ COMMITTED alone doesn't close this race (see #39 and #42): it doesn't stop two concurrent transactions from each reading the same under-limit count before either commits its insert. Closing that gap needs either SERIALIZABLE isolation with retry-on-40001 at the call site, or an explicit lock serializing the count-then-insert sequence per key. SERIALIZABLE was rejected here: it would be the first use of that isolation level anywhere in this codebase, it requires a retry loop at every call site (none exists yet), and it serializes on write-conflict detection after the fact rather than blocking upfront, which is a less direct fit for "don't let a second request even start counting until the first one's insert has committed" than a lock that does exactly that. The advisory lock is also the first use of its pattern in this codebase, but it needed no new supporting machinery beyond the one `withTx` transaction already available, and it holds up under Postgres' default isolation level rather than requiring a codebase-wide isolation-level change to introduce.

### Two locks, not one, and always acquired email-then-IP

The limiter has two independent dimensions (per-email, per-IP — see ADR-0016 for why both exist), and a single combined lock keyed on `(email, IP)` together wouldn't serialize a burst that varies one dimension while holding the other fixed (many IPs hammering one email, or one IP scanning many emails) — exactly the two abuse shapes ADR-0016 dimensions the limiter to catch. So the email dimension and the IP dimension each get their own lock, keyed independently, so two requests for unrelated emails and IPs still proceed fully in parallel.

Taking two locks per call introduces a lock-ordering hazard: if one caller could acquire email-then-IP while another acquired IP-then-email, two concurrent requests could deadlock (each holding the lock the other wants). `allowPasswordResetRequest` avoids this the standard way — a fixed global acquisition order, email first, then IP, with no code path that acquires them in the other order. `LockPasswordResetRequestKey`'s `classid` argument (email = 1, IP = 2) exists only to namespace the two lock spaces apart, so a 32-bit `hashtext` collision between an email string and an IP string can never let the two dimensions block each other by coincidence; it carries no meaning beyond that and isn't shared with any other lock user in this codebase.

## Considered Options

- **SERIALIZABLE isolation + retry-on-40001** — rejected: needs a retry loop that doesn't exist anywhere in this codebase yet, and detects the conflict after both transactions have already done the count-then-insert work, rather than blocking one of them upfront.
- **A single combined lock keyed on `(email, IP)`** — rejected: doesn't serialize the two abuse shapes (many IPs against one email, one IP against many emails) that the limiter's two independent dimensions exist to catch.
- **Reusing `internal/syncx`'s `KeyedMutex`** (already used to serialize `issuePasswordResetToken` per Employee, per ADR-0016) — rejected: it's an in-process `sync.Mutex` map, so it only serializes concurrent goroutines within one instance of this service; it does nothing for two requests landing on two different instances behind a load balancer, which an advisory lock (enforced by Postgres itself) does cover.

## Consequences

- `allowPasswordResetRequest` is no longer four unguarded round trips: it's one transaction, guarded by two advisory locks, so a concurrent burst for the same email or IP is now serialized rather than merely slowed — ADR-0016's 3/email and 10/IP figures are an exact ceiling per key, not just a deterrent.
- Requests for different emails and different IPs are unaffected and continue to run fully concurrently; only contention on the *same* email or *same* IP within the 15-minute window now blocks.
- Any future caller that needs a new per-key Postgres lock in this codebase should reuse `LockPasswordResetRequestKey`'s `classid` convention (pick an unused constant) rather than introduce a second, differently-shaped advisory-lock query.
