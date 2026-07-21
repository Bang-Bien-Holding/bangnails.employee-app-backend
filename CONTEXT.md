# Bangnails Employee App Backend

Backend for the Bangnails employee app: employee and store administration, plus the wifi/geofence data employees are checked against at login.

## Language

**Store**:
A physical salon location where employees work, synced one-way from Odoo (the source of truth for a store's existence, name, and city). `wifi_whitelist_enabled` gates only the Wifi Whitelist login check for that store — it is not a soft-delete tombstone and has no effect on the Geofence fallback; a store with it off is still a normal, fetchable, editable row an admin can re-enable at any time. It's purely admin-controlled: the Odoo sync never writes to it. A store that disappears from Odoo is hard-deleted (cascading to its Wifi Whitelist entries and to any Employee's membership rows for it), not soft-deleted.
_Avoid_: Location, branch, shop

**Employee**:
A person who works at one or more Stores. Identified internally by `id` (the stable primary key every other table and endpoint keys off) and, separately, by `odoo_employee_id` — the join key to Odoo's `hr.employee`, checked for existence whenever it's set or changed but never silently overwritten by that check. Odoo is the one-way source of truth only for an Employee's `fullName`, `email`, and Store membership, kept current by the existing sync job. `username` and Position are local-only: set at creation and changed only through this system, untouched by sync.
_Avoid_: Staff, worker, user

**Position**:
A local, admin-managed job title (e.g. "Technician", "Manager") an Employee can hold. An Employee can hold zero, one, or many Positions at once — never sourced from or synced with Odoo. Membership between Employee and Position is settable from either side — an Employee's full Position set, or a Position's full Employee set — always as a whole-set replacement via diff, never a single add/remove.
_Avoid_: Role, job title

**Wifi Whitelist**:
A store's known-good IP addresses and MAC addresses, kept as two independent lists (an IP whitelist and a MAC whitelist — no entry in one is paired with an entry in the other). Used to verify an employee's device is on the store's network at login. Replacing a list means diffing against the submitted set: values no longer present are removed, new values are inserted, unchanged values are left alone. A single entry can also be removed directly by its IP/MAC value (not by an internal id — a store never exposes per-entry ids) without touching the rest of the list; there is no whole-store delete, only whitelist-entry removal.
_Avoid_: IP list, MAC list, allowlist

**Geofence**:
A store's GPS fallback check (latitude, longitude, radius in meters), used to verify an employee's location when wifi verification is inconclusive.
_Avoid_: Location radius, geo-fence
