//go:build dbe2e

// End-to-end verification of GET /employees' search/filter query parameters
// (issue #28): q, position_ids, store_ids, odoo_employee_ids, is_active,
// and their combinations — plus sort order and the malformed/nonexistent-id
// edge cases the spec calls out. Every assertion goes through this
// application's own HTTP API (an httptest.Server wrapping the real router
// built by buildApplication), the same "no mocks in this seam" approach as
// cmd/login_e2e_test.go — the risk here is the SQL itself (dynamic optional
// filters, ILIKE substring matching, AND-across/OR-within combination, sort
// order), which only a real database can validate.
//
// This file holds setup and the tests themselves. The seams every test
// below crosses live in their own files: cmd/employees_search_e2e_client_test.go
// (loginE2EClient.List, the one HTTP method this suite adds) and
// cmd/employees_search_e2e_fixtures_test.go (the Postgres-facing
// employeesSearchE2EFixtures).
//
// GET /employees always returns the complete, unfiltered-by-default result
// set (issue #28: no pagination) — so without further care, every test here
// would also see whatever other Employee rows already exist in whichever
// Postgres this suite runs against. Every test sidesteps that by tagging its
// seeded Employees' full_name with a unique-per-subtest substring and always
// including that substring in q, so assertions are exact regardless of
// pre-existing data.
//
// Run it locally against the docker-compose Postgres with migrations
// applied:
//
//	go test -tags dbe2e -run TestEmployeesSearchE2E ./cmd/... -v
package main

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
)

// employeesSearchE2EResult mirrors employees.employeeResponse's JSON shape —
// only the fields this suite's assertions need.
type employeesSearchE2EResult struct {
	ID             int64   `json:"id"`
	OdooEmployeeID int64   `json:"odoo_employee_id"`
	FullName       string  `json:"full_name"`
	Email          string  `json:"email"`
	IsActive       bool    `json:"is_active"`
	PositionIDs    []int64 `json:"position_ids"`
	StoreIDs       []int64 `json:"store_ids"`
}

// employeesSearchE2ESetup builds on loginE2ESetup for the app-building,
// Postgres-reachability, and non-local-database-safety checks (see that
// function's doc comment) — none of that is specific to this suite, so it's
// reused rather than duplicated. It also reuses loginE2EFixtures.Employee
// (Admin: true) and loginE2EClient.MustLogin to obtain an Admin bearer
// token, since every test here needs one to call the AdminOnly-gated
// GET /employees, and loginE2EClient.List (cmd/employees_search_e2e_client_test.go)
// is what actually calls it. This suite's own
// employeesSearchE2EFixtures only adds what loginE2EFixtures doesn't
// already provide: direct control over FullName/Email/OdooEmployeeID/
// IsActive per Employee, plus Position/Store/link creation.
func employeesSearchE2ESetup(t *testing.T) (*loginE2EClient, *employeesSearchE2EFixtures, string) {
	t.Helper()

	loginClient, loginFixtures := loginE2ESetup(t)

	admin := loginFixtures.Employee(t, employeeSeed{Activated: true, Admin: true})
	lr := loginClient.MustLogin(t, admin.Username, admin.Password, 0, 0, "")

	fixtures := newEmployeesSearchE2EFixtures(loginFixtures.pool, loginFixtures.repo)

	return loginClient, fixtures, lr.Token
}

// employeesSearchE2EList calls client.List, requires a 200, and decodes the
// body into a []employeesSearchE2EResult — the "act" step almost every case
// below shares.
func employeesSearchE2EList(t *testing.T, client *loginE2EClient, token string, query url.Values) []employeesSearchE2EResult {
	t.Helper()

	resp := client.List(t, token, query)
	if resp.status != http.StatusOK {
		t.Fatalf("GET /employees?%s: expected 200, got %d: %s", query.Encode(), resp.status, resp.raw)
	}
	var results []employeesSearchE2EResult
	resp.decode(t, &results)
	return results
}

// employeesSearchE2EIDs extracts the id field from a result list, in order
// — what most assertions below compare against an expected id set.
func employeesSearchE2EIDs(results []employeesSearchE2EResult) []int64 {
	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids
}

func assertIDsEqual(t *testing.T, got []int64, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected ids %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected ids %v, got %v", want, got)
		}
	}
}

func containsID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// TestEmployeesSearchE2E_TextQuery covers q matching by full_name, by
// email, and matching neither.
func TestEmployeesSearchE2E_TextQuery(t *testing.T) {
	client, fixtures, token := employeesSearchE2ESetup(t)

	t.Run("matches by full_name", func(t *testing.T) {
		tag := e2eUnique(t, "qname")
		emp := fixtures.Employee(t, employeeSearchSeed{FullName: "Nguyen " + tag + " Van A"})

		results := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}})
		assertIDsEqual(t, employeesSearchE2EIDs(results), []int64{emp.ID})
	})

	t.Run("matches by email", func(t *testing.T) {
		tag := e2eUnique(t, "qemail")
		emp := fixtures.Employee(t, employeeSearchSeed{Email: tag + "@example.com"})

		results := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}})
		assertIDsEqual(t, employeesSearchE2EIDs(results), []int64{emp.ID})
	})

	t.Run("matches neither", func(t *testing.T) {
		tag := e2eUnique(t, "qnomatch")
		fixtures.Employee(t, employeeSearchSeed{FullName: "Someone Else " + e2eUnique(t, "other")})

		results := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}})
		if len(results) != 0 {
			t.Errorf("expected no matches for an unused tag, got %v", employeesSearchE2EIDs(results))
		}
	})
}

// TestEmployeesSearchE2E_PositionIDs covers position_ids with one id and
// with multiple ids (OR semantics), scoped with q so pre-existing Employees
// elsewhere in the database can't leak into the assertion.
func TestEmployeesSearchE2E_PositionIDs(t *testing.T) {
	client, fixtures, token := employeesSearchE2ESetup(t)
	tag := e2eUnique(t, "pos")

	posA := fixtures.Position(t)
	posB := fixtures.Position(t)

	empA := fixtures.Employee(t, employeeSearchSeed{FullName: "A " + tag})
	fixtures.LinkPosition(t, empA.ID, posA)

	empB := fixtures.Employee(t, employeeSearchSeed{FullName: "B " + tag})
	fixtures.LinkPosition(t, empB.ID, posB)

	empNone := fixtures.Employee(t, employeeSearchSeed{FullName: "C " + tag})

	t.Run("single id", func(t *testing.T) {
		results := employeesSearchE2EList(t, client, token, url.Values{
			"q":            {tag},
			"position_ids": {strconv.FormatInt(posA, 10)},
		})
		ids := employeesSearchE2EIDs(results)
		if len(ids) != 1 || ids[0] != empA.ID {
			t.Errorf("expected only empA (%d), got %v", empA.ID, ids)
		}
	})

	t.Run("multiple ids OR together", func(t *testing.T) {
		results := employeesSearchE2EList(t, client, token, url.Values{
			"q":            {tag},
			"position_ids": {strconv.FormatInt(posA, 10) + "," + strconv.FormatInt(posB, 10)},
		})
		ids := employeesSearchE2EIDs(results)
		if !containsID(ids, empA.ID) || !containsID(ids, empB.ID) || containsID(ids, empNone.ID) {
			t.Errorf("expected empA and empB but not empNone, got %v", ids)
		}
		if len(ids) != 2 {
			t.Errorf("expected exactly 2 matches, got %v", ids)
		}
	})
}

// TestEmployeesSearchE2E_StoreIDs covers store_ids with one id and with
// multiple ids (OR semantics) — the Store-side mirror of
// TestEmployeesSearchE2E_PositionIDs.
func TestEmployeesSearchE2E_StoreIDs(t *testing.T) {
	client, fixtures, token := employeesSearchE2ESetup(t)
	tag := e2eUnique(t, "store")

	storeA := fixtures.Store(t)
	storeB := fixtures.Store(t)

	empA := fixtures.Employee(t, employeeSearchSeed{FullName: "A " + tag})
	fixtures.LinkStore(t, empA.ID, storeA)

	empB := fixtures.Employee(t, employeeSearchSeed{FullName: "B " + tag})
	fixtures.LinkStore(t, empB.ID, storeB)

	empNone := fixtures.Employee(t, employeeSearchSeed{FullName: "C " + tag})

	t.Run("single id", func(t *testing.T) {
		results := employeesSearchE2EList(t, client, token, url.Values{
			"q":         {tag},
			"store_ids": {strconv.FormatInt(storeA, 10)},
		})
		ids := employeesSearchE2EIDs(results)
		if len(ids) != 1 || ids[0] != empA.ID {
			t.Errorf("expected only empA (%d), got %v", empA.ID, ids)
		}
	})

	t.Run("multiple ids OR together", func(t *testing.T) {
		results := employeesSearchE2EList(t, client, token, url.Values{
			"q":         {tag},
			"store_ids": {strconv.FormatInt(storeA, 10) + "," + strconv.FormatInt(storeB, 10)},
		})
		ids := employeesSearchE2EIDs(results)
		if !containsID(ids, empA.ID) || !containsID(ids, empB.ID) || containsID(ids, empNone.ID) {
			t.Errorf("expected empA and empB but not empNone, got %v", ids)
		}
		if len(ids) != 2 {
			t.Errorf("expected exactly 2 matches, got %v", ids)
		}
	})
}

// TestEmployeesSearchE2E_PositionAndStoreAreIndependentFacets confirms
// issue #28's user story 7: position_ids and store_ids combine as
// independent AND-across facets, never as "this Position specifically at
// that Store" — an Employee holding Position X at Store P (not Q) still
// matches position_ids=X&store_ids=Q, as long as they separately belong to
// Store Q via some other assignment.
func TestEmployeesSearchE2E_PositionAndStoreAreIndependentFacets(t *testing.T) {
	client, fixtures, token := employeesSearchE2ESetup(t)
	tag := e2eUnique(t, "indep")

	posManager := fixtures.Position(t)
	storeNimes := fixtures.Store(t)
	storeMontpellier := fixtures.Store(t)

	// emp holds posManager (assigned via no particular Store — Position
	// membership carries no Store scope at all, ADR-0008) and belongs to
	// storeNimes — but never held posManager "at" storeMontpellier, since no
	// such pairing exists in this schema.
	emp := fixtures.Employee(t, employeeSearchSeed{FullName: "Manager " + tag})
	fixtures.LinkPosition(t, emp.ID, posManager)
	fixtures.LinkStore(t, emp.ID, storeNimes)

	// Doesn't hold posManager, but does belong to storeNimes — must be
	// excluded once position_ids=posManager is added, proving the AND.
	empWrongPosition := fixtures.Employee(t, employeeSearchSeed{FullName: "NotManager " + tag})
	fixtures.LinkStore(t, empWrongPosition.ID, storeNimes)

	// Holds posManager, but belongs to storeMontpellier, not storeNimes —
	// must be excluded once store_ids=storeNimes is added.
	empWrongStore := fixtures.Employee(t, employeeSearchSeed{FullName: "OtherManager " + tag})
	fixtures.LinkPosition(t, empWrongStore.ID, posManager)
	fixtures.LinkStore(t, empWrongStore.ID, storeMontpellier)

	results := employeesSearchE2EList(t, client, token, url.Values{
		"q":            {tag},
		"position_ids": {strconv.FormatInt(posManager, 10)},
		"store_ids":    {strconv.FormatInt(storeNimes, 10)},
	})
	assertIDsEqual(t, employeesSearchE2EIDs(results), []int64{emp.ID})
}

// TestEmployeesSearchE2E_OdooEmployeeIDs covers odoo_employee_ids' exact-list
// match.
func TestEmployeesSearchE2E_OdooEmployeeIDs(t *testing.T) {
	client, fixtures, token := employeesSearchE2ESetup(t)
	tag := e2eUnique(t, "odoo")

	empA := fixtures.Employee(t, employeeSearchSeed{FullName: "A " + tag})
	empB := fixtures.Employee(t, employeeSearchSeed{FullName: "B " + tag})
	fixtures.Employee(t, employeeSearchSeed{FullName: "C " + tag}) // not in the filter list

	results := employeesSearchE2EList(t, client, token, url.Values{
		"q":                 {tag},
		"odoo_employee_ids": {strconv.FormatInt(empA.OdooEmployeeID, 10) + "," + strconv.FormatInt(empB.OdooEmployeeID, 10)},
	})
	ids := employeesSearchE2EIDs(results)
	if !containsID(ids, empA.ID) || !containsID(ids, empB.ID) {
		t.Errorf("expected empA and empB, got %v", ids)
	}
	if len(ids) != 2 {
		t.Errorf("expected exactly 2 matches, got %v", ids)
	}
}

// TestEmployeesSearchE2E_IsActive covers is_active=true, is_active=false,
// and omitted (both returned).
func TestEmployeesSearchE2E_IsActive(t *testing.T) {
	client, fixtures, token := employeesSearchE2ESetup(t)
	tag := e2eUnique(t, "active")

	active := fixtures.Employee(t, employeeSearchSeed{FullName: "Active " + tag})
	inactive := fixtures.Employee(t, employeeSearchSeed{FullName: "Inactive " + tag, IsActive: boolPtr(false)})

	t.Run("is_active=true", func(t *testing.T) {
		results := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}, "is_active": {"true"}})
		assertIDsEqual(t, employeesSearchE2EIDs(results), []int64{active.ID})
	})

	t.Run("is_active=false", func(t *testing.T) {
		results := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}, "is_active": {"false"}})
		assertIDsEqual(t, employeesSearchE2EIDs(results), []int64{inactive.ID})
	})

	t.Run("omitted returns both", func(t *testing.T) {
		results := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}})
		ids := employeesSearchE2EIDs(results)
		if !containsID(ids, active.ID) || !containsID(ids, inactive.ID) || len(ids) != 2 {
			t.Errorf("expected both active and inactive, got %v", ids)
		}
	})
}

// TestEmployeesSearchE2E_MalformedFilter covers a non-numeric entry in any
// id-list filter rejecting with 400 (user story 11).
func TestEmployeesSearchE2E_MalformedFilter(t *testing.T) {
	client, _, token := employeesSearchE2ESetup(t)

	cases := []struct {
		name  string
		query url.Values
	}{
		{name: "position_ids", query: url.Values{"position_ids": {"abc"}}},
		{name: "store_ids", query: url.Values{"store_ids": {"1,not-a-number"}}},
		{name: "odoo_employee_ids", query: url.Values{"odoo_employee_ids": {"xyz"}}},
		{name: "is_active", query: url.Values{"is_active": {"not-a-bool"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := client.List(t, token, c.query)
			if resp.status != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", resp.status, resp.raw)
			}
		})
	}
}

// TestEmployeesSearchE2E_NonexistentIDInFilter covers user story 12: a
// well-formed but nonexistent id in a filter list contributes nothing
// (rather than erroring), and the rest of the list still matches normally.
func TestEmployeesSearchE2E_NonexistentIDInFilter(t *testing.T) {
	client, fixtures, token := employeesSearchE2ESetup(t)
	tag := e2eUnique(t, "ghost")

	store := fixtures.Store(t)
	emp := fixtures.Employee(t, employeeSearchSeed{FullName: "Real " + tag})
	fixtures.LinkStore(t, emp.ID, store)

	// A deleted Position's id no longer exists in the positions table —
	// simulated here with a large, never-issued id rather than actually
	// deleting one (BIGSERIAL ids are never reused, so any sufficiently
	// large unused value behaves identically).
	const nonexistentID = int64(9223372036854775000)

	results := employeesSearchE2EList(t, client, token, url.Values{
		"q":         {tag},
		"store_ids": {strconv.FormatInt(store, 10) + "," + strconv.FormatInt(nonexistentID, 10)},
	})
	assertIDsEqual(t, employeesSearchE2EIDs(results), []int64{emp.ID})
}

// TestEmployeesSearchE2E_ZeroStoreAndZeroPosition covers user stories 8-9:
// an Employee with no Store (or Position) assignment still appears when
// that facet's filter is omitted, and correctly disappears once any filter
// for that facet is applied.
func TestEmployeesSearchE2E_ZeroStoreAndZeroPosition(t *testing.T) {
	client, fixtures, token := employeesSearchE2ESetup(t)

	t.Run("zero stores", func(t *testing.T) {
		tag := e2eUnique(t, "zerostore")
		unassigned := fixtures.Employee(t, employeeSearchSeed{FullName: "Unassigned " + tag})
		otherStore := fixtures.Store(t)

		withoutFilter := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}})
		if !containsID(employeesSearchE2EIDs(withoutFilter), unassigned.ID) {
			t.Errorf("expected the zero-Store Employee to appear when store_ids is omitted, got %v", employeesSearchE2EIDs(withoutFilter))
		}

		withFilter := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}, "store_ids": {strconv.FormatInt(otherStore, 10)}})
		if containsID(employeesSearchE2EIDs(withFilter), unassigned.ID) {
			t.Errorf("expected the zero-Store Employee to disappear once store_ids is applied, got %v", employeesSearchE2EIDs(withFilter))
		}
	})

	t.Run("zero positions", func(t *testing.T) {
		tag := e2eUnique(t, "zeroposition")
		unassigned := fixtures.Employee(t, employeeSearchSeed{FullName: "Unassigned " + tag})
		otherPosition := fixtures.Position(t)

		withoutFilter := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}})
		if !containsID(employeesSearchE2EIDs(withoutFilter), unassigned.ID) {
			t.Errorf("expected the zero-Position Employee to appear when position_ids is omitted, got %v", employeesSearchE2EIDs(withoutFilter))
		}

		withFilter := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}, "position_ids": {strconv.FormatInt(otherPosition, 10)}})
		if containsID(employeesSearchE2EIDs(withFilter), unassigned.ID) {
			t.Errorf("expected the zero-Position Employee to disappear once position_ids is applied, got %v", employeesSearchE2EIDs(withFilter))
		}
	})
}

// TestEmployeesSearchE2E_SortOrder covers user story 10: results are sorted
// alphabetically, case-insensitively, by full_name.
func TestEmployeesSearchE2E_SortOrder(t *testing.T) {
	client, fixtures, token := employeesSearchE2ESetup(t)
	tag := e2eUnique(t, "sort")

	// Deliberately mixed-case and out of alphabetical creation order — a
	// case-sensitive sort would put every uppercase-initial name before any
	// lowercase-initial one, which this asserts against.
	names := []string{"charlie", "Alice", "bob"}
	ids := make(map[string]int64, len(names))
	for _, name := range names {
		emp := fixtures.Employee(t, employeeSearchSeed{FullName: name + " " + tag})
		ids[name] = emp.ID
	}

	results := employeesSearchE2EList(t, client, token, url.Values{"q": {tag}})
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d: %v", len(results), results)
	}

	wantOrder := []int64{ids["Alice"], ids["bob"], ids["charlie"]}
	assertIDsEqual(t, employeesSearchE2EIDs(results), wantOrder)
}

// TestEmployeesSearchE2E_CombinedFilters covers a q + position_ids +
// store_ids + is_active query all applied together.
func TestEmployeesSearchE2E_CombinedFilters(t *testing.T) {
	client, fixtures, token := employeesSearchE2ESetup(t)
	tag := e2eUnique(t, "combined")

	position := fixtures.Position(t)
	store := fixtures.Store(t)

	match := fixtures.Employee(t, employeeSearchSeed{FullName: "Match " + tag})
	fixtures.LinkPosition(t, match.ID, position)
	fixtures.LinkStore(t, match.ID, store)

	wrongPosition := fixtures.Employee(t, employeeSearchSeed{FullName: "WrongPosition " + tag})
	fixtures.LinkStore(t, wrongPosition.ID, store)

	wrongStore := fixtures.Employee(t, employeeSearchSeed{FullName: "WrongStore " + tag})
	fixtures.LinkPosition(t, wrongStore.ID, position)

	inactiveMatch := fixtures.Employee(t, employeeSearchSeed{FullName: "InactiveMatch " + tag, IsActive: boolPtr(false)})
	fixtures.LinkPosition(t, inactiveMatch.ID, position)
	fixtures.LinkStore(t, inactiveMatch.ID, store)

	notMatchingQ := fixtures.Employee(t, employeeSearchSeed{FullName: "NoTagHere " + e2eUnique(t, "other")})
	fixtures.LinkPosition(t, notMatchingQ.ID, position)
	fixtures.LinkStore(t, notMatchingQ.ID, store)

	results := employeesSearchE2EList(t, client, token, url.Values{
		"q":            {tag},
		"position_ids": {strconv.FormatInt(position, 10)},
		"store_ids":    {strconv.FormatInt(store, 10)},
		"is_active":    {"true"},
	})
	assertIDsEqual(t, employeesSearchE2EIDs(results), []int64{match.ID})
}

// boolPtr is intentionally not reused from internal/employees' test helper
// of the same name — that one lives in a different package.
func boolPtr(b bool) *bool { return &b }
