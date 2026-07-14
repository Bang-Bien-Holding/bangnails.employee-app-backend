# 03 — PATCH /v1/stores/{id}: wifi IP/MAC whitelist replace

**What to build:** The same `PATCH /v1/stores/{id}` endpoint from ticket 02 now also accepts `ip_addresses` and `mac_addresses`, each independently replacing the store's full whitelist for that list to match exactly what's submitted. This is the "add or remove entries by editing the comma-separated field and saving" behavior from the mockup screen.

**Blocked by:** 02 (extends the same PATCH handler, service method, and transaction scaffold built there)

**Status:** ready-for-agent

- [ ] Request body gains `ip_addresses` (array of strings) and `mac_addresses` (array of strings), each optional and independent of the other and of the geofence group.
- [ ] Omitting `ip_addresses` (or `mac_addresses`) entirely leaves that whitelist untouched. Submitting it — including as `[]` — replaces the store's entire whitelist for that list: values not in the submitted set are deleted, values in the submitted set that don't already exist are inserted, unchanged values are left alone (no unnecessary delete+reinsert).
- [ ] `ip_addresses` elements are validated as IPv4 format; `mac_addresses` elements as MAC-48 format. A malformed element returns `400`.
- [ ] A duplicate value within the same `ip_addresses` array (or within `mac_addresses`) is rejected with `400`, not silently deduped.
- [ ] The replace runs in the same transaction as the geofence update and the `updated_at` concurrency check from ticket 02 — a PATCH that touches wifi lists but not geofence still goes through the same `updated_at` match-or-409 check, and still bumps `updated_at` on success.
- [ ] The success response's `ip_addresses`/`mac_addresses` reflect the post-replace state, using the same response shape as tickets 01/02.
- [ ] Service seam: tests against a mocked `repo.Querier` cover omitted-list-untouched (replace query not invoked) vs. present-list-triggers-replace (including empty array clearing the list), and confirm the geofence-only and wifi-only PATCH paths both still enforce the `updated_at` check from ticket 02.
- [ ] Handler seam: tests against a mocked `Service` cover `400` for malformed IP/MAC format and in-array duplicates, and a success case exercising both lists together.
