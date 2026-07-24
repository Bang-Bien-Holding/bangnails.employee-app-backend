//go:build dbe2e

// End-to-end verification of GET /v1/stores' search/filter query parameters
// (issues #32, #33, #34): store_name, city, wifi_whitelist_enabled,
// odoo_store_ids, and their combinations — plus sort order and the
// malformed/nonexistent-id edge cases each ticket calls out. Every assertion
// goes through this application's own HTTP API (an httptest.Server wrapping
// the real router built by buildApplication), the same "no mocks in this
// seam" approach as cmd/employees_search_e2e_test.go — the risk here is the
// SQL itself (dynamic optional filters, ILIKE substring matching,
// AND-across combination, sort order), which only a real database can
// validate.
//
// This file holds setup and the tests themselves. The seams every test
// below crosses live in their own files: cmd/stores_search_e2e_client_test.go
// (loginE2EClient.ListStores, the one HTTP method this suite adds) and
// cmd/stores_search_e2e_fixtures_test.go (the Postgres-facing
// storesSearchE2EFixtures).
//
// GET /v1/stores always returns the complete, unfiltered-by-default result
// set (no pagination) — so without further care, every test here would also
// see whatever other Store rows already exist in whichever Postgres this
// suite runs against. Every test sidesteps that by tagging its seeded
// Stores' store_name with a unique-per-subtest substring and always
// including that substring in store_name, so assertions are exact
// regardless of pre-existing data.
//
// Run it locally against the docker-compose Postgres with migrations
// applied:
//
//	go test -tags dbe2e -run TestStoresSearchE2E ./cmd/... -v
package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// storesSearchE2EResult mirrors stores.storeResponse's JSON shape — only
// the fields this suite's assertions need.
type storesSearchE2EResult struct {
	ID                   int64  `json:"id"`
	StoreName            string `json:"store_name"`
	City                 string `json:"city"`
	WifiWhitelistEnabled bool   `json:"wifi_whitelist_enabled"`
}

// storesSearchE2ESetup builds on loginE2ESetup for the app-building,
// Postgres-reachability, and non-local-database-safety checks (see that
// function's doc comment) — none of that is specific to this suite, so it's
// reused rather than duplicated. It also reuses loginE2EFixtures.Employee
// (Admin: true) and loginE2EClient.MustLogin to obtain an Admin bearer
// token, since every test here needs one to call the AdminOnly-gated
// GET /v1/stores, and loginE2EClient.ListStores
// (cmd/stores_search_e2e_client_test.go) is what actually calls it. This
// suite's own storesSearchE2EFixtures only adds what loginE2EFixtures
// doesn't already provide: direct control over StoreName/City/
// WifiWhitelistEnabled/OdooStoreID per Store.
func storesSearchE2ESetup(t *testing.T) (*loginE2EClient, *storesSearchE2EFixtures, string) {
	t.Helper()

	loginClient, loginFixtures := loginE2ESetup(t)

	admin := loginFixtures.Employee(t, employeeSeed{Activated: true, Admin: true})
	lr := loginClient.MustLogin(t, admin.Username, admin.Password, 0, 0, "")

	fixtures := newStoresSearchE2EFixtures(loginFixtures.pool, loginFixtures.repo)

	return loginClient, fixtures, lr.Token
}

// storesSearchE2EList calls client.ListStores, requires a 200, and decodes
// the body into a []storesSearchE2EResult — the "act" step almost every
// case below shares.
func storesSearchE2EList(t *testing.T, client *loginE2EClient, token string, query url.Values) []storesSearchE2EResult {
	t.Helper()

	resp := client.ListStores(t, token, query)
	if resp.status != http.StatusOK {
		t.Fatalf("GET /v1/stores?%s: expected 200, got %d: %s", query.Encode(), resp.status, resp.raw)
	}
	var results []storesSearchE2EResult
	resp.decode(t, &results)
	return results
}

// storesSearchE2EIDs extracts the id field from a result list, in order —
// what most assertions below compare against an expected id set.
func storesSearchE2EIDs(results []storesSearchE2EResult) []int64 {
	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids
}

var odooStoreIDCounter atomic.Int64

// e2eUniqueOdooStoreID returns a short unique odoo_store_id value — the
// store table's odoo_store_id column is VARCHAR(20), too short for
// e2eUnique's "e2e-<label>-<unixnano>" format, so this instead base36-encodes
// a nanosecond timestamp (avoiding collision with real Odoo-synced ids in
// whichever Postgres this suite runs against, same goal as e2eUnique) plus a
// per-process counter (guarding against two calls landing in the same
// nanosecond).
func e2eUniqueOdooStoreID(t *testing.T) string {
	t.Helper()
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(odooStoreIDCounter.Add(1), 36)
}

// TestStoresSearchE2E_StoreName covers store_name matching a substring,
// case-insensitively, and matching nothing.
func TestStoresSearchE2E_StoreName(t *testing.T) {
	client, fixtures, token := storesSearchE2ESetup(t)

	t.Run("matches a substring case-insensitively", func(t *testing.T) {
		tag := e2eUnique(t, "name")
		store := fixtures.Store(t, storeSearchSeed{StoreName: "Hanoi " + tag + " Branch"})

		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {strings.ToUpper(tag)}})
		assertIDsEqual(t, storesSearchE2EIDs(results), []int64{store.ID})
	})

	t.Run("matches neither", func(t *testing.T) {
		tag := e2eUnique(t, "namenomatch")
		fixtures.Store(t, storeSearchSeed{StoreName: "Someone Else " + e2eUnique(t, "other")})

		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}})
		if len(results) != 0 {
			t.Errorf("expected no matches for an unused tag, got %v", storesSearchE2EIDs(results))
		}
	})
}

// TestStoresSearchE2E_City covers city matching a substring, and
// city omitted so an unset-city store's absence from a city-filtered result
// doesn't error (NULL city ILIKE anything is NULL/falsy).
func TestStoresSearchE2E_City(t *testing.T) {
	client, fixtures, token := storesSearchE2ESetup(t)
	tag := e2eUnique(t, "city")

	cityTag := "City" + tag
	inCity := fixtures.Store(t, storeSearchSeed{StoreName: "Store A " + tag, City: &cityTag})
	noCity := fixtures.Store(t, storeSearchSeed{StoreName: "Store B " + tag})

	t.Run("matches by city", func(t *testing.T) {
		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}, "city": {cityTag}})
		assertIDsEqual(t, storesSearchE2EIDs(results), []int64{inCity.ID})
	})

	t.Run("omitted returns both", func(t *testing.T) {
		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}})
		ids := storesSearchE2EIDs(results)
		if !containsID(ids, inCity.ID) || !containsID(ids, noCity.ID) || len(ids) != 2 {
			t.Errorf("expected both stores, got %v", ids)
		}
	})
}

// TestStoresSearchE2E_StoreNameAndCityAreAndedFacets covers store_name and
// city combining as AND-across facets — a Store matching only one of the
// two must not appear once both are supplied.
func TestStoresSearchE2E_StoreNameAndCityAreAndedFacets(t *testing.T) {
	client, fixtures, token := storesSearchE2ESetup(t)
	tag := e2eUnique(t, "and")
	cityTag := "City" + tag

	match := fixtures.Store(t, storeSearchSeed{StoreName: "Match " + tag, City: &cityTag})
	wrongCity := fixtures.Store(t, storeSearchSeed{StoreName: "Match " + tag, City: strPtr("Other" + tag)})
	wrongName := fixtures.Store(t, storeSearchSeed{StoreName: "NoMatch " + e2eUnique(t, "other"), City: &cityTag})

	results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}, "city": {cityTag}})
	ids := storesSearchE2EIDs(results)
	if containsID(ids, wrongCity.ID) || containsID(ids, wrongName.ID) {
		t.Errorf("expected wrongCity/wrongName excluded, got %v", ids)
	}
	assertIDsEqual(t, ids, []int64{match.ID})
}

// TestStoresSearchE2E_WifiWhitelistEnabled covers wifi_whitelist_enabled's
// exact-match filtering (issue #33): true, false, and omitted (both
// returned).
func TestStoresSearchE2E_WifiWhitelistEnabled(t *testing.T) {
	client, fixtures, token := storesSearchE2ESetup(t)
	tag := e2eUnique(t, "wifi")

	enabled := fixtures.Store(t, storeSearchSeed{StoreName: "Enabled " + tag, WifiWhitelistEnabled: boolPtr(true)})
	disabled := fixtures.Store(t, storeSearchSeed{StoreName: "Disabled " + tag, WifiWhitelistEnabled: boolPtr(false)})

	t.Run("wifi_whitelist_enabled=true", func(t *testing.T) {
		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}, "wifi_whitelist_enabled": {"true"}})
		assertIDsEqual(t, storesSearchE2EIDs(results), []int64{enabled.ID})
	})

	t.Run("wifi_whitelist_enabled=false", func(t *testing.T) {
		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}, "wifi_whitelist_enabled": {"false"}})
		assertIDsEqual(t, storesSearchE2EIDs(results), []int64{disabled.ID})
	})

	t.Run("omitted returns both", func(t *testing.T) {
		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}})
		ids := storesSearchE2EIDs(results)
		if !containsID(ids, enabled.ID) || !containsID(ids, disabled.ID) || len(ids) != 2 {
			t.Errorf("expected both enabled and disabled, got %v", ids)
		}
	})
}

// TestStoresSearchE2E_MalformedFilter covers wifi_whitelist_enabled holding
// something ParseBool doesn't recognize, rejecting with 400 (issue #33).
func TestStoresSearchE2E_MalformedFilter(t *testing.T) {
	client, _, token := storesSearchE2ESetup(t)

	resp := client.ListStores(t, token, url.Values{"wifi_whitelist_enabled": {"not-a-bool"}})
	if resp.status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", resp.status, resp.raw)
	}
}

// TestStoresSearchE2E_SortOrder covers issue #32's sort requirement: results
// are sorted alphabetically, case-insensitively, by store_name.
func TestStoresSearchE2E_SortOrder(t *testing.T) {
	client, fixtures, token := storesSearchE2ESetup(t)
	tag := e2eUnique(t, "sort")

	// Deliberately mixed-case and out of alphabetical creation order — a
	// case-sensitive sort would put every uppercase-initial name before any
	// lowercase-initial one, which this asserts against.
	names := []string{"charlie", "Alice", "bob"}
	ids := make(map[string]int64, len(names))
	for _, name := range names {
		store := fixtures.Store(t, storeSearchSeed{StoreName: name + " " + tag})
		ids[name] = store.ID
	}

	results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}})
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d: %v", len(results), results)
	}

	wantOrder := []int64{ids["Alice"], ids["bob"], ids["charlie"]}
	assertIDsEqual(t, storesSearchE2EIDs(results), wantOrder)
}

// TestStoresSearchE2E_OdooStoreIDs covers odoo_store_ids' exact-match,
// OR-within-list filtering (issue #34): a single id, multiple ids ORed
// together, a well-formed but nonexistent id contributing nothing without
// erroring, and omitted returning everything matching the other filters.
func TestStoresSearchE2E_OdooStoreIDs(t *testing.T) {
	client, fixtures, token := storesSearchE2ESetup(t)

	t.Run("matches by a single id", func(t *testing.T) {
		tag := e2eUnique(t, "odoo1")
		id := e2eUniqueOdooStoreID(t)
		store := fixtures.Store(t, storeSearchSeed{StoreName: "Odoo " + tag, OdooStoreID: &id})

		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}, "odoo_store_ids": {id}})
		assertIDsEqual(t, storesSearchE2EIDs(results), []int64{store.ID})
	})

	t.Run("OR within the list", func(t *testing.T) {
		tag := e2eUnique(t, "odooor")
		id1 := e2eUniqueOdooStoreID(t)
		id2 := e2eUniqueOdooStoreID(t)
		store1 := fixtures.Store(t, storeSearchSeed{StoreName: "Odoo1 " + tag, OdooStoreID: &id1})
		store2 := fixtures.Store(t, storeSearchSeed{StoreName: "Odoo2 " + tag, OdooStoreID: &id2})

		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}, "odoo_store_ids": {id1 + "," + id2}})
		ids := storesSearchE2EIDs(results)
		if !containsID(ids, store1.ID) || !containsID(ids, store2.ID) || len(ids) != 2 {
			t.Errorf("expected both stores, got %v", ids)
		}
	})

	t.Run("nonexistent id contributes nothing, rest still matches", func(t *testing.T) {
		tag := e2eUnique(t, "odooghost")
		id := e2eUniqueOdooStoreID(t)
		store := fixtures.Store(t, storeSearchSeed{StoreName: "Odoo " + tag, OdooStoreID: &id})

		// A well-formed but never-issued odoo_store_id, under the
		// VARCHAR(20) limit — guaranteed not to collide with any seeded or
		// real Odoo-synced id.
		const nonexistentID = "e2e-nonexistent-id"

		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}, "odoo_store_ids": {id + "," + nonexistentID}})
		assertIDsEqual(t, storesSearchE2EIDs(results), []int64{store.ID})
	})

	t.Run("omitted returns everything matching other filters", func(t *testing.T) {
		tag := e2eUnique(t, "odooomit")
		id := e2eUniqueOdooStoreID(t)
		withID := fixtures.Store(t, storeSearchSeed{StoreName: "WithID " + tag, OdooStoreID: &id})
		withoutID := fixtures.Store(t, storeSearchSeed{StoreName: "WithoutID " + tag})

		results := storesSearchE2EList(t, client, token, url.Values{"store_name": {tag}})
		ids := storesSearchE2EIDs(results)
		if !containsID(ids, withID.ID) || !containsID(ids, withoutID.ID) || len(ids) != 2 {
			t.Errorf("expected both stores, got %v", ids)
		}
	})
}

// TestStoresSearchE2E_CombinedFilters covers store_name + city +
// wifi_whitelist_enabled + odoo_store_ids all applied together (issue #34's
// explicit combined-filters acceptance criterion) — one decoy per facet,
// each failing only that facet, proves AND-across-facets holds for all four.
func TestStoresSearchE2E_CombinedFilters(t *testing.T) {
	client, fixtures, token := storesSearchE2ESetup(t)
	tag := e2eUnique(t, "combined")
	cityTag := "City" + tag

	matchID := e2eUniqueOdooStoreID(t)
	wrongCityID := e2eUniqueOdooStoreID(t)
	wrongWifiID := e2eUniqueOdooStoreID(t)
	wrongOdooID := e2eUniqueOdooStoreID(t)
	wrongNameID := e2eUniqueOdooStoreID(t)

	match := fixtures.Store(t, storeSearchSeed{StoreName: "Match " + tag, City: &cityTag, WifiWhitelistEnabled: boolPtr(true), OdooStoreID: &matchID})
	wrongCity := fixtures.Store(t, storeSearchSeed{StoreName: "Match " + tag, City: strPtr("Other" + tag), WifiWhitelistEnabled: boolPtr(true), OdooStoreID: &wrongCityID})
	wrongWifi := fixtures.Store(t, storeSearchSeed{StoreName: "Match " + tag, City: &cityTag, WifiWhitelistEnabled: boolPtr(false), OdooStoreID: &wrongWifiID})
	wrongOdoo := fixtures.Store(t, storeSearchSeed{StoreName: "Match " + tag, City: &cityTag, WifiWhitelistEnabled: boolPtr(true), OdooStoreID: &wrongOdooID})
	wrongName := fixtures.Store(t, storeSearchSeed{StoreName: "NoMatch " + e2eUnique(t, "other"), City: &cityTag, WifiWhitelistEnabled: boolPtr(true), OdooStoreID: &wrongNameID})

	results := storesSearchE2EList(t, client, token, url.Values{
		"store_name":             {tag},
		"city":                   {cityTag},
		"wifi_whitelist_enabled": {"true"},
		// Deliberately omits wrongOdoo's own id, so it's the one decoy
		// excluded via the odoo_store_ids facet specifically.
		"odoo_store_ids": {matchID + "," + wrongCityID + "," + wrongWifiID + "," + wrongNameID},
	})
	ids := storesSearchE2EIDs(results)
	if containsID(ids, wrongCity.ID) || containsID(ids, wrongWifi.ID) || containsID(ids, wrongOdoo.ID) || containsID(ids, wrongName.ID) {
		t.Errorf("expected only the fully-matching store, got %v", ids)
	}
	assertIDsEqual(t, ids, []int64{match.ID})
}

// strPtr is intentionally not reused from internal/stores' test helpers —
// those live in a different package.
func strPtr(s string) *string { return &s }
