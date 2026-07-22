//go:build e2e

// This file implements the end-to-end verification called for by GitHub
// issue #9 ("07 — End-to-end verification against real Odoo"): confirming
// real OAuth2 authentication, employee existence validation, and store
// sync actually work against the live erp.bangnails.fr instance, not just
// against a fake/mock. Every assertion goes through this application's own
// HTTP API (an httptest.Server wrapping the real router built by
// buildApplication) — it never calls internal/odoo or the service layer
// directly, so a failure here means the deployed API itself is broken
// against real Odoo, not just an internal unit.
//
// Excluded from the default build/test/vet via the "e2e" build tag, since
// it needs a live Postgres (internal/adapters/postgresql/migrations
// applied) and a real, reachable Odoo instance with valid credentials —
// neither of which CI provides. Run it locally with the app's usual .env
// loaded:
//
//	go test -tags e2e -run TestE2E ./cmd/... -v
//
// E2E_KNOWN_ODOO_EMPLOYEE_ID optionally overrides the real hr.employee id
// used for the "known id" half of the existence-validation check; it
// defaults to 1 (Odoo's built-in Administrator, confirmed present on
// erp.bangnails.fr by this repo's own local dev history).
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

const (
	e2eKnownOdooEmployeeIDEnv      = "E2E_KNOWN_ODOO_EMPLOYEE_ID"
	e2eOtherKnownOdooEmployeeIDEnv = "E2E_OTHER_KNOWN_ODOO_EMPLOYEE_ID"
	e2eSyncTimeout                 = 30 * time.Second

	// e2eUnlikelyOdooEmployeeID stands in for an id Odoo has never heard
	// of. Odoo's own hr.employee ids are small sequential integers, so a
	// 12-digit value is safe from ever colliding with a real one.
	e2eUnlikelyOdooEmployeeID int64 = 999999999999
)

// e2eSetup skips the test entirely if ODOO_BASE_URL isn't configured (the
// default for `go test ./...` and CI, where this build-tagged file isn't
// even compiled — this guard is a second line of defense for anyone
// running `go test -tags e2e` without a real .env loaded), then builds the
// real application — real Postgres pool, real mailer, real Odoo HTTP
// client — exactly as main() does, and serves it via httptest.Server so
// every test interacts with it purely over HTTP. Returns the server's
// client and its /v1-prefixed base URL.
func e2eSetup(t *testing.T) (*http.Client, string) {
	t.Helper()

	if os.Getenv("ODOO_BASE_URL") == "" {
		t.Skip("ODOO_BASE_URL not set — skipping e2e verification against real Odoo (see issue #9); load .env and re-run with -tags e2e")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := buildApplication(context.Background(), logger)
	if err != nil {
		t.Fatalf("buildApplication() error = %v (is postgres running and are ODOO_BASE_URL/ODOO_CLIENT_ID/ODOO_CLIENT_SECRET/ODOO_USERNAME/ODOO_PASSWORD/ODOO_DATABASE set? see .env)", err)
	}
	t.Cleanup(func() { app.db.Close() })

	server := httptest.NewServer(app.mount())
	t.Cleanup(server.Close)

	return server.Client(), server.URL + "/v1"
}

// e2eWaitForSyncDone polls a {syncing bool} status endpoint until it
// reports done, or fails the test after e2eSyncTimeout — a stalled sync
// almost always means the Odoo call itself is stuck or erroring silently
// (runSync only logs, per internal/employees/service.go), so timing out is
// preferable to hanging.
func e2eWaitForSyncDone(t *testing.T, client *http.Client, statusURL string) {
	t.Helper()

	deadline := time.Now().Add(e2eSyncTimeout)
	for {
		resp := e2eRequest(t, client, http.MethodGet, statusURL, nil)
		if resp.status != http.StatusOK {
			t.Fatalf("GET %s: expected 200, got %d: %s", statusURL, resp.status, resp.raw)
		}
		var status struct {
			Syncing bool `json:"syncing"`
		}
		resp.decode(t, &status)
		if !status.Syncing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync at %s did not finish within %s", statusURL, e2eSyncTimeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func e2eKnownOdooEmployeeID(t *testing.T) int64 {
	t.Helper()

	raw := os.Getenv(e2eKnownOdooEmployeeIDEnv)
	if raw == "" {
		return 1
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("%s=%q is not a valid int64", e2eKnownOdooEmployeeIDEnv, raw)
	}
	return id
}

// e2eOtherKnownOdooEmployeeID returns a second real, currently-existing
// hr.employee id, distinct from knownID, for the update-path half of
// TestE2E_EmployeeExistenceValidation (UpdateEmployee only re-validates
// when odooEmployeeId is actually changing — ADR-0007 — so that check
// needs two different real ids, not one reused). Defaults to 5, confirmed
// present on erp.bangnails.fr by this repo's own local dev history.
func e2eOtherKnownOdooEmployeeID(t *testing.T, knownID int64) int64 {
	t.Helper()

	raw := os.Getenv(e2eOtherKnownOdooEmployeeIDEnv)
	id := int64(5)
	if raw != "" {
		var err error
		id, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q is not a valid int64", e2eOtherKnownOdooEmployeeIDEnv, raw)
		}
	}
	if id == knownID {
		t.Skipf("%s (%d) must differ from %s (%d) to exercise UpdateEmployee's odooEmployeeId-changed path", e2eOtherKnownOdooEmployeeIDEnv, id, e2eKnownOdooEmployeeIDEnv, knownID)
	}
	return id
}

type e2eEmployee struct {
	ID             int64   `json:"id"`
	OdooEmployeeID int64   `json:"odoo_employee_id"`
	FullName       string  `json:"full_name"`
	Email          string  `json:"email"`
	Username       string  `json:"username"`
	StoreIDs       []int64 `json:"store_ids"`
}

func e2eGetEmployee(t *testing.T, client *http.Client, base string, id int64) e2eEmployee {
	t.Helper()

	resp := e2eRequest(t, client, http.MethodGet, fmt.Sprintf("%s/employees/%d", base, id), nil)
	if resp.status != http.StatusOK {
		t.Fatalf("GET /employees/%d: expected 200, got %d: %s", id, resp.status, resp.raw)
	}
	var employee e2eEmployee
	resp.decode(t, &employee)
	return employee
}

// TestE2E_TokenAuthAndStoreSync exercises issue #9's first two acceptance
// criteria at once: triggering a store sync only succeeds end-to-end if a
// real access token was obtained via the OAuth2 password grant and the
// subsequent search_read against Odoo's pos.shop model (ADR-0009) worked,
// so a populated store list is proof of both.
func TestE2E_TokenAuthAndStoreSync(t *testing.T) {
	client, base := e2eSetup(t)

	triggerResp := e2eRequest(t, client, http.MethodPost, base+"/stores/syncs", nil)
	if triggerResp.status != http.StatusAccepted && triggerResp.status != http.StatusConflict {
		t.Fatalf("POST /stores/syncs: expected 202 (or 409 if a sync from another test is already running), got %d: %s — check the OAuth2 password grant against %s in internal/odoo/http_client.go", triggerResp.status, triggerResp.raw, os.Getenv("ODOO_BASE_URL"))
	}

	e2eWaitForSyncDone(t, client, base+"/stores/syncs")

	listResp := e2eRequest(t, client, http.MethodGet, base+"/stores", nil)
	if listResp.status != http.StatusOK {
		t.Fatalf("GET /stores: expected 200, got %d: %s", listResp.status, listResp.raw)
	}
	var stores []struct {
		ID        int64  `json:"id"`
		StoreName string `json:"store_name"`
	}
	listResp.decode(t, &stores)

	if len(stores) == 0 {
		t.Error("expected at least one store after syncing against real Odoo, got none — either erp.bangnails.fr genuinely has no pos.shop records, or the sync silently failed (check server logs)")
	}
	t.Logf("synced %d store(s) from real Odoo: %+v", len(stores), stores)
}

// TestE2E_EmployeeExistenceValidation covers issue #9's second acceptance
// criterion: a known real Odoo employee id passes CreateEmployee's
// existence check, an unknown one is rejected (ADR-0007).
func TestE2E_EmployeeExistenceValidation(t *testing.T) {
	client, base := e2eSetup(t)

	t.Run("unknown id is rejected", func(t *testing.T) {
		resp := e2eRequest(t, client, http.MethodPost, base+"/employees", map[string]any{
			"odooEmployeeId": e2eUnlikelyOdooEmployeeID,
			"fullName":       "E2E Nonexistent Employee",
			"email":          e2eUnique(t, "nonexistent") + "@example.com",
			"username":       e2eUnique(t, "nonexistent"),
		})
		if resp.status != http.StatusBadRequest {
			t.Fatalf("POST /employees with odoo id %d (assumed not real): expected 400 (ErrOdooEmployeeIDNotFound), got %d: %s", e2eUnlikelyOdooEmployeeID, resp.status, resp.raw)
		}
	})

	t.Run("known real id passes existence validation", func(t *testing.T) {
		knownID := e2eKnownOdooEmployeeID(t)

		resp := e2eRequest(t, client, http.MethodPost, base+"/employees", map[string]any{
			"odooEmployeeId": knownID,
			"fullName":       "E2E Known Employee",
			"email":          e2eUnique(t, "known") + "@example.com",
			"username":       e2eUnique(t, "known"),
		})

		switch resp.status {
		case http.StatusCreated:
			// No row in this dev DB used knownID yet — clean up what this
			// test just created so re-runs stay idempotent.
			var created struct {
				ID int64 `json:"id"`
			}
			resp.decode(t, &created)
			del := e2eRequest(t, client, http.MethodDelete, fmt.Sprintf("%s/employees/%d", base, created.ID), nil)
			if del.status != http.StatusNoContent {
				t.Errorf("cleanup: DELETE /employees/%d: expected 204, got %d: %s", created.ID, del.status, del.raw)
			}
		case http.StatusConflict:
			// knownID already belongs to an existing row in this dev DB.
			// That still proves the Odoo existence check passed: a failed
			// check (ErrOdooEmployeeIDNotFound) returns 400 before the
			// request ever reaches the DB's uniqueness constraint — see
			// CreateEmployee in internal/employees/service.go, which calls
			// validateOdooEmployeeID before the insert.
		case http.StatusBadRequest:
			t.Fatalf("POST /employees with known-real odoo id %d: got 400 (existence check rejected it) — either %d is no longer real on %s, or %s needs to be set to a currently-real id", knownID, knownID, os.Getenv("ODOO_BASE_URL"), e2eKnownOdooEmployeeIDEnv)
		default:
			t.Fatalf("POST /employees with known-real odoo id %d: unexpected status %d: %s", knownID, resp.status, resp.raw)
		}
	})

	// The manual checklist this suite replaces called out both PATCH/PUT
	// and POST — UpdateEmployee is a distinct code path from CreateEmployee
	// (it only re-validates when odooEmployeeId is actually changing, per
	// ADR-0007), so it needs its own coverage against the real instance.
	t.Run("existence validation on update", func(t *testing.T) {
		knownID := e2eKnownOdooEmployeeID(t)
		otherKnownID := e2eOtherKnownOdooEmployeeID(t, knownID)

		employeeID := e2eEnsureEmployee(t, client, base, knownID)
		current := e2eGetEmployee(t, client, base, employeeID)

		t.Run("unknown id is rejected", func(t *testing.T) {
			resp := e2eRequest(t, client, http.MethodPut, fmt.Sprintf("%s/employees/%d", base, employeeID), map[string]any{
				"odooEmployeeId": e2eUnlikelyOdooEmployeeID,
				"fullName":       current.FullName,
				"email":          current.Email,
				"username":       current.Username,
			})
			if resp.status != http.StatusBadRequest {
				t.Fatalf("PUT /employees/%d changing odooEmployeeId to %d (assumed not real): expected 400, got %d: %s", employeeID, e2eUnlikelyOdooEmployeeID, resp.status, resp.raw)
			}
		})

		t.Run("known real id passes existence validation", func(t *testing.T) {
			resp := e2eRequest(t, client, http.MethodPut, fmt.Sprintf("%s/employees/%d", base, employeeID), map[string]any{
				"odooEmployeeId": otherKnownID,
				"fullName":       current.FullName,
				"email":          current.Email,
				"username":       current.Username,
			})
			switch resp.status {
			case http.StatusOK:
				// otherKnownID wasn't already taken by another row — the
				// update went through for real. Revert it so this test
				// doesn't leave the dev DB's employee mutated.
				revert := e2eRequest(t, client, http.MethodPut, fmt.Sprintf("%s/employees/%d", base, employeeID), map[string]any{
					"odooEmployeeId": knownID,
					"fullName":       current.FullName,
					"email":          current.Email,
					"username":       current.Username,
				})
				if revert.status != http.StatusOK {
					t.Errorf("cleanup: revert PUT /employees/%d back to odoo id %d: expected 200, got %d: %s", employeeID, knownID, revert.status, revert.raw)
				}
			case http.StatusConflict:
				// otherKnownID already belongs to another row in this dev
				// DB — still proves the Odoo existence check passed, same
				// reasoning as the create-path 409 case above.
			case http.StatusBadRequest:
				t.Fatalf("PUT /employees/%d changing odooEmployeeId to known-real id %d: got 400 (existence check rejected it) — either %d is no longer real on %s, or %s needs to be set to a currently-real id", employeeID, otherKnownID, otherKnownID, os.Getenv("ODOO_BASE_URL"), e2eOtherKnownOdooEmployeeIDEnv)
			default:
				t.Fatalf("PUT /employees/%d changing odooEmployeeId to known-real id %d: unexpected status %d: %s", employeeID, otherKnownID, resp.status, resp.raw)
			}
		})
	})
}

// e2eEnsureEmployee returns the internal id of an employee row backed by
// odooEmployeeID, creating one via the API if none exists yet, or reusing
// the existing one (see the 409 case in TestE2E_EmployeeExistenceValidation
// for why a conflict here still means the row is real). If it creates one,
// it's cleaned up at the end of the test.
func e2eEnsureEmployee(t *testing.T, client *http.Client, base string, odooEmployeeID int64) int64 {
	t.Helper()

	createResp := e2eRequest(t, client, http.MethodPost, base+"/employees", map[string]any{
		"odooEmployeeId": odooEmployeeID,
		"fullName":       "E2E Sync Target",
		"email":          e2eUnique(t, "sync-target") + "@example.com",
		"username":       e2eUnique(t, "sync-target"),
	})

	switch createResp.status {
	case http.StatusCreated:
		var created struct {
			ID int64 `json:"id"`
		}
		createResp.decode(t, &created)
		t.Cleanup(func() {
			e2eRequest(t, client, http.MethodDelete, fmt.Sprintf("%s/employees/%d", base, created.ID), nil)
		})
		return created.ID

	case http.StatusConflict:
		listResp := e2eRequest(t, client, http.MethodGet, base+"/employees", nil)
		if listResp.status != http.StatusOK {
			t.Fatalf("GET /employees: expected 200, got %d: %s", listResp.status, listResp.raw)
		}
		var all []struct {
			ID             int64 `json:"id"`
			OdooEmployeeID int64 `json:"odoo_employee_id"`
		}
		listResp.decode(t, &all)
		for _, e := range all {
			if e.OdooEmployeeID == odooEmployeeID {
				return e.ID
			}
		}
		t.Fatalf("POST /employees for odoo id %d returned 409 (already exists) but GET /employees found no matching row", odooEmployeeID)

	default:
		t.Fatalf("POST /employees for odoo id %d: expected 201 or 409, got %d: %s", odooEmployeeID, createResp.status, createResp.raw)
	}
	return 0
}

// TestE2E_EmployeeSyncPopulatesStoreMembership covers issue #9's third
// acceptance criterion: running employee sync against the real instance
// correctly populates store membership (or confirms none currently exist).
func TestE2E_EmployeeSyncPopulatesStoreMembership(t *testing.T) {
	client, base := e2eSetup(t)
	knownID := e2eKnownOdooEmployeeID(t)

	// Store membership only resolves against stores already synced
	// (ADR-0009: employee_stores rows are resolved via store.odoo_store_id),
	// so sync stores first.
	storeSync := e2eRequest(t, client, http.MethodPost, base+"/stores/syncs", nil)
	if storeSync.status != http.StatusAccepted && storeSync.status != http.StatusConflict {
		t.Fatalf("POST /stores/syncs: expected 202 or 409, got %d: %s", storeSync.status, storeSync.raw)
	}
	e2eWaitForSyncDone(t, client, base+"/stores/syncs")

	// The set of stores real Odoo returned just now — every store id the
	// employee sync attaches to this employee below must resolve to one of
	// these, or ADR-0009's odoo_store_id -> store.id resolution has a bug
	// (a stale/unresolvable id would otherwise pass silently, since the
	// acceptance criteria allow an employee to have zero stores).
	storesResp := e2eRequest(t, client, http.MethodGet, base+"/stores", nil)
	if storesResp.status != http.StatusOK {
		t.Fatalf("GET /stores: expected 200, got %d: %s", storesResp.status, storesResp.raw)
	}
	var stores []struct {
		ID int64 `json:"id"`
	}
	storesResp.decode(t, &stores)
	validStoreIDs := make(map[int64]bool, len(stores))
	for _, s := range stores {
		validStoreIDs[s.ID] = true
	}

	employeeID := e2eEnsureEmployee(t, client, base, knownID)

	syncResp := e2eRequest(t, client, http.MethodPost, base+"/employees/syncs", map[string]any{"ids": []int64{employeeID}})
	if syncResp.status != http.StatusAccepted && syncResp.status != http.StatusConflict {
		t.Fatalf("POST /employees/syncs: expected 202 or 409, got %d: %s", syncResp.status, syncResp.raw)
	}
	e2eWaitForSyncDone(t, client, base+"/employees/syncs")

	employee := e2eGetEmployee(t, client, base, employeeID)

	if employee.FullName == "" || employee.Email == "" {
		t.Errorf("expected sync to have overwritten full_name/email from Odoo (ADR-0007), got %+v", employee)
	}
	if employee.StoreIDs == nil {
		t.Errorf("expected store_ids to be an empty list, not null, per the always-non-nil convention (see EmployeeDetail in internal/employees/types.go)")
	}
	for _, id := range employee.StoreIDs {
		if !validStoreIDs[id] {
			t.Errorf("employee %d's synced store_ids contains %d, which isn't among the stores GET /stores just returned (%v) — ADR-0009's store id resolution looks broken", employeeID, id, stores)
		}
	}
	t.Logf("employee id=%d (odoo_employee_id=%d) after sync: full_name=%q email=%q store_ids=%v — if store_ids is empty, confirm directly in Odoo whether this employee genuinely has no multi-store assignment (issue #9's acceptance criteria allows either outcome)", employeeID, knownID, employee.FullName, employee.Email, employee.StoreIDs)
}
