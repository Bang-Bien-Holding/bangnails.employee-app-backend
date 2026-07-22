---
status: accepted
---

# Login's presence check tries IP, then Geofence, then Wifi Whitelist MAC — trust-ordered, not request-ordered

Login was originally specified to check MAC first, then IP, then Geofence. But a Wifi Whitelist MAC entry can only ever be matched against a self-report from the Employee's device — a web server never sees a Wi-Fi access point's BSSID, since it's link-layer information that doesn't survive routing over the internet. Reading it at all requires a native app calling OS-level Wi-Fi APIs (Android's `WifiManager`, gated behind location permission; iOS's `NEHotspotHelper`, gated behind an Apple entitlement general business apps are unlikely to qualify for) — and even then, it's a claim the app itself could misreport. The IP check, by contrast, is the server-observed source address of the request itself: nothing the client sends can change what address the connection actually came from.

Putting the least trustworthy, least portable, native-app-only check first, ahead of two more reliable ones, had no upside once this asymmetry was surfaced.

## Decision

Login's presence check runs per Store the Employee belongs to (see `CONTEXT.md`'s Login and Store entries), in this order, stopping at the first match:

1. **IP** — the server-observed request source IP compared against the Store's Wifi Whitelist IP entries. Not client-reported; the app sends no IP field for this check.
2. **Geofence** — the Employee's device-reported latitude/longitude checked against the Store's Geofence radius. Requires device location permission, but no special OS entitlement.
3. **Wifi Whitelist MAC** — the BSSID of the Wi-Fi network the device is currently connected to, self-reported by the native app via OS Wi-Fi APIs, checked against the Store's Wifi Whitelist MAC entries. The only one of the three a modified client could lie about outright, and the only one requiring native OS APIs — evaluated last, as a best-effort signal, not a primary check.

The first Store (among the Employee's Store memberships) where any tier matches wins; that Store is recorded on the resulting Session (see [ADR-0014](/docs/adr/0014-session-lifecycle-opaque-tokens-heartbeat-logout.md)). Heartbeat reruns the same three-tier check against that same Store. None of this applies to an Employee holding the Admin Position — they skip the check entirely (see [ADR-0015](/docs/adr/0015-admin-position-gates-login-bypass-and-admin-endpoints.md)).

## Considered Options

- Keeping MAC-first as originally specified — rejected once the trust asymmetry was clear: MAC is the one tier that's both spoofable and native-app-only, so checking it first buys nothing over checking it last, and costs reliability (Android/iOS permission gaps make it the least available of the three).

## Consequences

- The app must request location permission to reach even the second tier — it isn't optional for a device that fails the IP check.
- MAC support may end up effectively Android-only in practice, since Apple's `NEHotspotHelper` entitlement isn't granted to general business apps; MAC failing is a normal, expected outcome, not an error condition.
- A reverse proxy in front of the API must be configured so only a trusted hop's `X-Forwarded-For`/`X-Real-IP` is honored — otherwise the IP check's unspoofability guarantee breaks.
