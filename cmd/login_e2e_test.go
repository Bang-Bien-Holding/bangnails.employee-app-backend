//go:build dbe2e

// End-to-end verification of Login/Logout/Heartbeat/AdminOnly (ADR-0013,
// ADR-0014, ADR-0015, issues #20-#25) plus the activation flow a real first
// login depends on — every assertion goes through this application's own
// HTTP API (an httptest.Server wrapping the real router built by
// buildApplication), never internal/auth or internal/employees directly.
//
// This file holds setup and the tests themselves. The two seams every test
// below crosses live in their own files: cmd/login_e2e_client_test.go (the
// HTTP-facing loginE2EClient) and cmd/login_e2e_fixtures_test.go (the
// Postgres-facing loginE2EFixtures).
//
// Unlike cmd/e2e_test.go's Odoo suite, none of this touches Odoo at all —
// Login/Logout/Heartbeat/Activate are pure Postgres + bcrypt + this
// package's own HTTP layer — so this suite runs under its own "dbe2e" build
// tag, gated only on a reachable Postgres (via loginE2ESetup's Skip), not on
// ODOO_BASE_URL. That's what lets it run in CI (see
// .github/workflows/backend-ci.yml's login-e2e job) instead of being
// permanently opt-in-only like the Odoo suite.
//
// Run it locally against the docker-compose Postgres with migrations
// applied:
//
//	go test -tags dbe2e -run TestLoginE2E ./cmd/... -v
//	go test -tags dbe2e -run TestLogoutE2E ./cmd/... -v
//	go test -tags dbe2e -run TestHeartbeatE2E ./cmd/... -v
//	go test -tags dbe2e -run TestAdminOnlyGatingE2E ./cmd/... -v
//	go test -tags dbe2e -run TestActivationThenLoginE2E ./cmd/... -v
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixed geo-coordinates used across the presence-check cases: loginE2EStore*
// is "at the store" (Paris), loginE2EFar* is far enough away (Sydney) that
// no store radius any test here sets could accidentally cover both.
const (
	loginE2EStoreLat  = 48.8566
	loginE2EStoreLong = 2.3522
	loginE2ERadius    = int32(100)

	loginE2EFarLat  = -33.8688
	loginE2EFarLong = 151.2093

	// loginE2ELoopbackIP is the client IP middleware.ClientIPFromRemoteAddr
	// (cmd/api.go) reads back for every request this suite makes:
	// httptest.NewServer listens on 127.0.0.1, so the TCP connection's
	// source address is always this — deterministic, no network mocking
	// needed to exercise the IP presence tier for real (ADR-0013).
	loginE2ELoopbackIP = "127.0.0.1"
	// loginE2EUnwhitelistedIP stands in for a client IP no Store's Wifi
	// Whitelist will ever contain in this suite.
	loginE2EUnwhitelistedIP = "10.0.0.99"

	loginE2EMatchingMAC = "aa:bb:cc:dd:ee:01"

	loginE2EDefaultPassword = "correct-horse-battery-staple"
)

// loginE2ECheckOnce runs the "is this suite even runnable" check exactly
// once per binary run, no matter how many of the 9 top-level tests call
// loginE2ESetup — a misconfigured/unreachable DATABASE_DSN then costs one
// connection attempt (and, if it's a hang rather than a fast refusal, one
// timeout) instead of nine. Every test still gets its own fresh
// *application/pool/httptest.Server below, exactly as before — only the
// reachability/safety check itself is shared, not the runtime resources.
var (
	loginE2ECheckOnce   sync.Once
	loginE2ESkipReason  string
	loginE2EFatalReason string
)

// loginE2ESetup builds the real application exactly as e2eSetup does (real
// Postgres pool, real router), but skips only when Postgres itself isn't
// reachable — see the file doc comment for why that's a different gate than
// cmd/e2e_test.go's ODOO_BASE_URL check. Every test's only touchpoints are
// the two values this returns: the Client for HTTP, the Fixtures for
// Postgres.
func loginE2ESetup(t *testing.T) (*loginE2EClient, *loginE2EFixtures) {
	t.Helper()

	loginE2ECheckOnce.Do(func() {
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		app, err := buildApplication(context.Background(), logger)
		if err != nil {
			loginE2ESkipReason = fmt.Sprintf("Postgres not reachable — skipping login e2e suite: %v (run `docker compose up -d postgres` with migrations applied, or set DATABASE_DSN)", err)
			return
		}
		defer app.db.Close()
		loginE2EFatalReason = loginE2ENonLocalDatabaseReason(app.db)
	})

	if loginE2ESkipReason != "" {
		t.Skip(loginE2ESkipReason)
	}
	if loginE2EFatalReason != "" {
		t.Fatal(loginE2EFatalReason)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := buildApplication(context.Background(), logger)
	if err != nil {
		// loginE2ECheckOnce already confirmed Postgres is reachable — this
		// would mean it became unreachable between then and now.
		t.Skipf("Postgres not reachable — skipping login e2e suite: %v (run `docker compose up -d postgres` with migrations applied, or set DATABASE_DSN)", err)
	}
	t.Cleanup(func() { app.db.Close() })

	server := httptest.NewServer(app.mount())
	t.Cleanup(server.Close)

	client := newLoginE2EClient(server.Client(), server.URL+"/v1")
	fixtures := newLoginE2EFixtures(app.db, repo.New(app.db))
	return client, fixtures
}

// loginE2ENonLocalDatabaseReason reports why pool must not be used by this
// suite, or "" if it's fine — empty means a loopback-reachable Postgres
// host. Both contexts this suite is actually meant for — docker-compose
// locally, and the CI job's service container (also exposed via localhost,
// since GitHub Actions runs service containers on the runner's own network)
// — are loopback. This suite creates and deletes real rows on whatever
// DATABASE_DSN resolves to, so a misconfigured/copy-pasted DSN pointing at a
// shared or production database must fail loudly, not silently start
// writing to it. Returns a reason string rather than calling t.Fatal
// directly — loginE2ECheckOnce's body runs without a *testing.T of its own,
// since it runs once for whichever test happens to trigger it first; each
// caller still fails on its own, with its own accurate pass/fail status.
func loginE2ENonLocalDatabaseReason(pool *pgxpool.Pool) string {
	host := pool.Config().ConnConfig.Host
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return ""
	}
	return fmt.Sprintf("refusing to run login e2e suite against non-loopback Postgres host %q — this suite creates and deletes real rows, so DATABASE_DSN must point at a local/CI-only database (docker-compose's postgres service, or the CI job's service container), never a shared or production one", host)
}

// loginE2ENonAdminSession seeds an activated, non-Admin Employee at a Store
// whose geofence covers loginE2EStoreLat/Long, links them, and logs in —
// the shared "arrange" step for Heartbeat/Logout cases that need a real,
// Store-bound Session to act on.
func loginE2ENonAdminSession(t *testing.T, client *loginE2EClient, fixtures *loginE2EFixtures) (token string, employeeID, storeID int64) {
	t.Helper()

	employee := fixtures.Employee(t, employeeSeed{Activated: true})
	storeID = fixtures.Store(t, storeSeed{
		Latitude:     floatPtr(loginE2EStoreLat),
		Longitude:    floatPtr(loginE2EStoreLong),
		RadiusMeters: int32Ptr(loginE2ERadius),
	})
	fixtures.Link(t, employee.ID, storeID)

	lr := client.MustLogin(t, employee.Username, employee.Password, loginE2EStoreLat, loginE2EStoreLong, "")
	return lr.Token, employee.ID, storeID
}

// TestLoginE2E_PresenceTiers covers cases 1-3 and 16: ADR-0013's
// IP-then-Geofence-then-MAC trust order, and wifi_whitelist_enabled=false
// gating only the IP/MAC tiers, not Geofence.
func TestLoginE2E_PresenceTiers(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	t.Run("ip tier match", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})
		storeID := fixtures.Store(t, storeSeed{IPs: []string{loginE2ELoopbackIP}})
		fixtures.Link(t, employee.ID, storeID)

		lr := client.MustLogin(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
		if lr.StoreID == nil || *lr.StoreID != storeID {
			t.Errorf("expected store_id=%d, got %v", storeID, lr.StoreID)
		}
	})

	t.Run("geofence tier match", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})
		storeID := fixtures.Store(t, storeSeed{
			IPs:          []string{loginE2EUnwhitelistedIP},
			Latitude:     floatPtr(loginE2EStoreLat),
			Longitude:    floatPtr(loginE2EStoreLong),
			RadiusMeters: int32Ptr(loginE2ERadius),
		})
		fixtures.Link(t, employee.ID, storeID)

		lr := client.MustLogin(t, employee.Username, employee.Password, loginE2EStoreLat, loginE2EStoreLong, "")
		if lr.StoreID == nil || *lr.StoreID != storeID {
			t.Errorf("expected store_id=%d, got %v", storeID, lr.StoreID)
		}
	})

	t.Run("mac tier match", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})
		storeID := fixtures.Store(t, storeSeed{
			IPs:  []string{loginE2EUnwhitelistedIP},
			MACs: []string{loginE2EMatchingMAC},
		})
		fixtures.Link(t, employee.ID, storeID)

		lr := client.MustLogin(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, loginE2EMatchingMAC)
		if lr.StoreID == nil || *lr.StoreID != storeID {
			t.Errorf("expected store_id=%d, got %v", storeID, lr.StoreID)
		}
	})

	t.Run("wifi whitelist disabled still allows geofence match", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})
		storeID := fixtures.Store(t, storeSeed{
			WifiWhitelistDisabled: true,
			IPs:                   []string{loginE2ELoopbackIP},
			Latitude:              floatPtr(loginE2EStoreLat),
			Longitude:             floatPtr(loginE2EStoreLong),
			RadiusMeters:          int32Ptr(loginE2ERadius),
		})
		fixtures.Link(t, employee.ID, storeID)

		lr := client.MustLogin(t, employee.Username, employee.Password, loginE2EStoreLat, loginE2EStoreLong, "")
		if lr.StoreID == nil || *lr.StoreID != storeID {
			t.Errorf("expected geofence match to still succeed with wifi_whitelist_enabled=false, got store_id=%v", lr.StoreID)
		}
	})

	t.Run("wifi whitelist disabled skips ip tier", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})
		// IP would match if wifi_whitelist_enabled were true; disabled and
		// no geofence/MAC means no tier can match at all.
		storeID := fixtures.Store(t, storeSeed{
			WifiWhitelistDisabled: true,
			IPs:                   []string{loginE2ELoopbackIP},
		})
		fixtures.Link(t, employee.ID, storeID)

		resp := client.Login(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
		if resp.status != http.StatusForbidden {
			t.Errorf("expected 403 (ErrNoStoreMatch) once wifi_whitelist_enabled=false disables the matching IP entry, got %d: %s", resp.status, resp.raw)
		}
	})
}

// TestLoginE2E_AdminBypass covers case 4: an Admin Employee's Login skips
// the presence check entirely (ADR-0015) — no Store membership needed at
// all.
func TestLoginE2E_AdminBypass(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	employee := fixtures.Employee(t, employeeSeed{Activated: true, Admin: true})

	lr := client.MustLogin(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
	if lr.StoreID != nil {
		t.Errorf("expected store_id=nil for an Admin Session, got %d", *lr.StoreID)
	}
}

// TestLoginE2E_InvalidCredentials covers cases 5, 6, 8: unknown username,
// wrong password, and a deactivated Employee all collapse into the same
// generic ErrInvalidCredentials/401 (see auth.ErrInvalidCredentials's
// comment on why).
func TestLoginE2E_InvalidCredentials(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	t.Run("unknown username", func(t *testing.T) {
		resp := client.Login(t, e2eUnique(t, "nobody"), "whatever-password", loginE2EFarLat, loginE2EFarLong, "")
		if resp.status != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d: %s", resp.status, resp.raw)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})
		resp := client.Login(t, employee.Username, "the-wrong-password", loginE2EFarLat, loginE2EFarLong, "")
		if resp.status != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d: %s", resp.status, resp.raw)
		}
	})

	t.Run("deactivated employee", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true, Inactive: true})
		resp := client.Login(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
		if resp.status != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d: %s", resp.status, resp.raw)
		}
	})
}

// TestLoginE2E_NotActivated covers case 7: an Employee who exists but has
// never completed POST /v1/activate gets a distinct, non-generic error.
func TestLoginE2E_NotActivated(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	employee := fixtures.Employee(t, employeeSeed{Activated: false})

	resp := client.Login(t, employee.Username, "any-password-at-all", loginE2EFarLat, loginE2EFarLong, "")
	if resp.status != http.StatusForbidden {
		t.Errorf("expected 403 (ErrAccountNotActivated), got %d: %s", resp.status, resp.raw)
	}
}

// TestLoginE2E_Lockout covers cases 9, 10, 11, 14: 5 consecutive wrong
// passwords locks the account for 15 minutes (issue #21); a locked account
// rejects even the correct password; once locked_until has passed, the
// correct password succeeds again and resets the counter.
func TestLoginE2E_Lockout(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	t.Run("5th wrong attempt locks the account, correct password still rejected", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true})

		for i := 1; i <= 5; i++ {
			resp := client.Login(t, employee.Username, "wrong-password", loginE2EFarLat, loginE2EFarLong, "")
			if resp.status != http.StatusUnauthorized {
				t.Fatalf("wrong-password attempt %d: expected 401, got %d: %s", i, resp.status, resp.raw)
			}
		}

		state := fixtures.EmployeeLockState(t, employee.ID)
		if !state.LockedUntil.Valid || !state.LockedUntil.Time.After(time.Now()) {
			t.Fatalf("expected locked_until to be set in the future after 5 failed attempts, got %+v", state.LockedUntil)
		}

		resp := client.Login(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
		if resp.status != http.StatusUnauthorized {
			t.Errorf("correct password while locked: expected 401, got %d: %s", resp.status, resp.raw)
		}
	})

	t.Run("lockout expires, correct password succeeds and resets the counter", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{
			Activated:           true,
			Admin:               true, // sidesteps needing a matching Store for the post-unlock login
			FailedLoginAttempts: 5,
			LockedUntil:         timePtr(time.Now().Add(time.Hour)),
		})

		stillLocked := client.Login(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
		if stillLocked.status != http.StatusUnauthorized {
			t.Fatalf("expected 401 while still locked, got %d: %s", stillLocked.status, stillLocked.raw)
		}

		fixtures.SetLockedUntil(t, employee.ID, time.Now().Add(-time.Minute))

		resp := client.Login(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200 once locked_until has passed, got %d: %s", resp.status, resp.raw)
		}

		state := fixtures.EmployeeLockState(t, employee.ID)
		if state.FailedAttempts != 0 {
			t.Errorf("expected failed_login_attempts reset to 0, got %d", state.FailedAttempts)
		}
		if state.LockedUntil.Valid {
			t.Errorf("expected locked_until cleared, got %+v", state.LockedUntil)
		}
	})
}

// TestLoginE2E_ResetsFailedAttempts covers case 14 in isolation: a
// successful login resets a failure count that never reached the lockout
// threshold.
func TestLoginE2E_ResetsFailedAttempts(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	employee := fixtures.Employee(t, employeeSeed{
		Activated:           true,
		Admin:               true,
		FailedLoginAttempts: 3,
	})

	resp := client.Login(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
	if resp.status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.status, resp.raw)
	}

	state := fixtures.EmployeeLockState(t, employee.ID)
	if state.FailedAttempts != 0 {
		t.Errorf("expected failed_login_attempts reset to 0 after a successful login, got %d", state.FailedAttempts)
	}
}

// TestLoginE2E_NoStoreMatch covers case 12: correct credentials, but no
// Store the Employee belongs to matches on any presence tier.
func TestLoginE2E_NoStoreMatch(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	employee := fixtures.Employee(t, employeeSeed{Activated: true})
	storeID := fixtures.Store(t, storeSeed{IPs: []string{loginE2EUnwhitelistedIP}})
	fixtures.Link(t, employee.ID, storeID)

	resp := client.Login(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
	if resp.status != http.StatusForbidden {
		t.Errorf("expected 403 (ErrNoStoreMatch), got %d: %s", resp.status, resp.raw)
	}
}

// TestLoginE2E_Validation covers case 13. None of these need a real
// Employee — go-playground/validator rejects the request before the
// service layer ever runs a lookup. These bodies are deliberately malformed
// or incomplete, so they go through RawLogin rather than Login's typed
// signature.
func TestLoginE2E_Validation(t *testing.T) {
	client, _ := loginE2ESetup(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "missing username",
			body: map[string]any{
				"password": "irrelevant", "latitude": loginE2EFarLat, "longitude": loginE2EFarLong,
			},
		},
		{
			name: "missing password",
			body: map[string]any{
				"username": "irrelevant", "latitude": loginE2EFarLat, "longitude": loginE2EFarLong,
			},
		},
		{
			name: "missing latitude",
			body: map[string]any{
				"username": "irrelevant", "password": "irrelevant", "longitude": loginE2EFarLong,
			},
		},
		{
			name: "missing longitude",
			body: map[string]any{
				"username": "irrelevant", "password": "irrelevant", "latitude": loginE2EFarLat,
			},
		},
		{
			name: "malformed mac_address",
			body: map[string]any{
				"username": "irrelevant", "password": "irrelevant", "latitude": loginE2EFarLat, "longitude": loginE2EFarLong, "mac_address": "not-a-mac",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := client.RawLogin(t, c.body)
			if resp.status != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", resp.status, resp.raw)
			}
		})
	}
}

// TestLoginE2E_NewLoginSupersedesSession covers case 15 (ADR-0014's
// single-active-Session rule): a second Login for the same Employee
// atomically replaces the first Session — the old token stops resolving to
// anything at all.
func TestLoginE2E_NewLoginSupersedesSession(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	employee := fixtures.Employee(t, employeeSeed{Activated: true, Admin: true})

	first := client.MustLogin(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
	second := client.MustLogin(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
	if first.Token == second.Token {
		t.Fatalf("expected a fresh token on the second Login, got the same token twice")
	}

	oldHB := client.Heartbeat(t, first.Token, loginE2EFarLat, loginE2EFarLong, "")
	if oldHB.status != http.StatusOK {
		t.Fatalf("heartbeat with superseded token: expected 200, got %d: %s", oldHB.status, oldHB.raw)
	}
	var oldResult loginE2EHeartbeatResponse
	oldHB.decode(t, &oldResult)
	if oldResult.Active || oldResult.Reason != "logged_out_elsewhere" {
		t.Errorf("expected superseded token to report {active:false, reason:logged_out_elsewhere}, got %+v", oldResult)
	}

	newHB := client.Heartbeat(t, second.Token, loginE2EFarLat, loginE2EFarLong, "")
	if newHB.status != http.StatusOK {
		t.Fatalf("heartbeat with current token: expected 200, got %d: %s", newHB.status, newHB.raw)
	}
	var newResult loginE2EHeartbeatResponse
	newHB.decode(t, &newResult)
	if !newResult.Active {
		t.Errorf("expected the current token's Session to still be active, got %+v", newResult)
	}
}

// TestLogoutE2E covers cases 17-19.
func TestLogoutE2E(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	t.Run("valid token ends the session", func(t *testing.T) {
		token, _, _ := loginE2ENonAdminSession(t, client, fixtures)

		resp := client.Logout(t, token)
		if resp.status != http.StatusNoContent {
			t.Fatalf("expected 204, got %d: %s", resp.status, resp.raw)
		}

		if fixtures.SessionExists(t, token) {
			t.Errorf("expected the session row to be deleted after Logout")
		}
	})

	t.Run("idempotent for a token naming no open session", func(t *testing.T) {
		resp := client.Logout(t, "never-issued-token-value")
		if resp.status != http.StatusNoContent {
			t.Errorf("expected 204 (idempotent), got %d: %s", resp.status, resp.raw)
		}
	})

	t.Run("missing authorization header", func(t *testing.T) {
		resp := client.Logout(t, "")
		if resp.status != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d: %s", resp.status, resp.raw)
		}
	})
}

// TestHeartbeatE2E covers cases 20-29 (ADR-0014, issue #23).
func TestHeartbeatE2E(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	t.Run("passing presence check stays active", func(t *testing.T) {
		token, _, _ := loginE2ENonAdminSession(t, client, fixtures)

		resp := client.Heartbeat(t, token, loginE2EStoreLat, loginE2EStoreLong, "")
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.status, resp.raw)
		}
		var result loginE2EHeartbeatResponse
		resp.decode(t, &result)
		if !result.Active {
			t.Errorf("expected active:true, got %+v", result)
		}
	})

	t.Run("one failure stays active, two consecutive failures end the session", func(t *testing.T) {
		token, _, _ := loginE2ENonAdminSession(t, client, fixtures)

		first := client.Heartbeat(t, token, loginE2EFarLat, loginE2EFarLong, "")
		if first.status != http.StatusOK {
			t.Fatalf("failure 1: expected 200, got %d: %s", first.status, first.raw)
		}
		var firstResult loginE2EHeartbeatResponse
		first.decode(t, &firstResult)
		if !firstResult.Active {
			t.Fatalf("failure 1: expected active:true (below threshold), got %+v", firstResult)
		}
		if got := fixtures.SessionConsecutiveFailures(t, token); got != 1 {
			t.Errorf("failure 1: expected consecutive_failures=1, got %d", got)
		}

		second := client.Heartbeat(t, token, loginE2EFarLat, loginE2EFarLong, "")
		if second.status != http.StatusOK {
			t.Fatalf("failure 2: expected 200, got %d: %s", second.status, second.raw)
		}
		var secondResult loginE2EHeartbeatResponse
		second.decode(t, &secondResult)
		if secondResult.Active || secondResult.Reason != "left_premises" {
			t.Errorf("failure 2: expected {active:false, reason:left_premises}, got %+v", secondResult)
		}
		if fixtures.SessionExists(t, token) {
			t.Errorf("expected the session row deleted after 2 consecutive failures")
		}
	})

	t.Run("a passing heartbeat between failures resets the counter", func(t *testing.T) {
		token, _, _ := loginE2ENonAdminSession(t, client, fixtures)

		client.Heartbeat(t, token, loginE2EFarLat, loginE2EFarLong, "")
		if got := fixtures.SessionConsecutiveFailures(t, token); got != 1 {
			t.Fatalf("after 1 failure: expected consecutive_failures=1, got %d", got)
		}

		pass := client.Heartbeat(t, token, loginE2EStoreLat, loginE2EStoreLong, "")
		var passResult loginE2EHeartbeatResponse
		pass.decode(t, &passResult)
		if !passResult.Active {
			t.Fatalf("expected the passing heartbeat to report active:true, got %+v", passResult)
		}
		if got := fixtures.SessionConsecutiveFailures(t, token); got != 0 {
			t.Fatalf("after a pass: expected consecutive_failures reset to 0, got %d", got)
		}

		resp := client.Heartbeat(t, token, loginE2EFarLat, loginE2EFarLong, "")
		var result loginE2EHeartbeatResponse
		resp.decode(t, &result)
		if !result.Active {
			t.Errorf("expected the session to survive a single failure after the counter reset, got %+v", result)
		}
	})

	t.Run("absolute TTL expiry ends the session", func(t *testing.T) {
		token, _, _ := loginE2ENonAdminSession(t, client, fixtures)
		fixtures.SetSessionExpiresAt(t, token, time.Now().Add(-time.Minute))

		resp := client.Heartbeat(t, token, loginE2EStoreLat, loginE2EStoreLong, "")
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.status, resp.raw)
		}
		var result loginE2EHeartbeatResponse
		resp.decode(t, &result)
		if result.Active || result.Reason != "session_expired" {
			t.Errorf("expected {active:false, reason:session_expired}, got %+v", result)
		}
		if fixtures.SessionExists(t, token) {
			t.Errorf("expected the session row deleted after absolute TTL expiry")
		}
	})

	t.Run("silence timeout ends the session", func(t *testing.T) {
		token, _, _ := loginE2ENonAdminSession(t, client, fixtures)
		fixtures.SetSessionLastHeartbeatAt(t, token, time.Now().Add(-2*time.Minute))

		resp := client.Heartbeat(t, token, loginE2EStoreLat, loginE2EStoreLong, "")
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.status, resp.raw)
		}
		var result loginE2EHeartbeatResponse
		resp.decode(t, &result)
		if result.Active || result.Reason != "session_expired" {
			t.Errorf("expected {active:false, reason:session_expired} (90s silence backstop), got %+v", result)
		}
		if fixtures.SessionExists(t, token) {
			t.Errorf("expected the session row deleted after the silence timeout")
		}
	})

	t.Run("unresolvable token reports logged_out_elsewhere, not an error", func(t *testing.T) {
		resp := client.Heartbeat(t, "never-issued-token-value", loginE2EFarLat, loginE2EFarLong, "")
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200 (not 401) for an unresolvable token, got %d: %s", resp.status, resp.raw)
		}
		var result loginE2EHeartbeatResponse
		resp.decode(t, &result)
		if result.Active || result.Reason != "logged_out_elsewhere" {
			t.Errorf("expected {active:false, reason:logged_out_elsewhere}, got %+v", result)
		}
	})

	t.Run("admin session heartbeat is a no-op", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true, Admin: true})
		lr := client.MustLogin(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")

		resp := client.Heartbeat(t, lr.Token, loginE2EFarLat, loginE2EFarLong, "")
		if resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.status, resp.raw)
		}
		var result loginE2EHeartbeatResponse
		resp.decode(t, &result)
		if !result.Active {
			t.Errorf("expected an Admin Session's heartbeat to always report active:true regardless of location, got %+v", result)
		}
		if !fixtures.SessionExists(t, lr.Token) {
			t.Errorf("expected the Admin Session to still exist")
		}
	})

	t.Run("missing authorization header", func(t *testing.T) {
		resp := client.Heartbeat(t, "", loginE2EFarLat, loginE2EFarLong, "")
		if resp.status != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d: %s", resp.status, resp.raw)
		}
	})

	t.Run("validation error", func(t *testing.T) {
		token, _, _ := loginE2ENonAdminSession(t, client, fixtures)
		resp := client.RawHeartbeat(t, token, map[string]any{"longitude": loginE2EFarLong})
		if resp.status != http.StatusBadRequest {
			t.Errorf("expected 400 for a missing latitude, got %d: %s", resp.status, resp.raw)
		}
	})
}

// TestAdminOnlyGatingE2E covers cases 30-33 (ADR-0015, issue #25), exercised
// through one real admin route (GET /v1/employees) rather than the
// employees package's own logic — the gate itself, not what it protects, is
// the point.
func TestAdminOnlyGatingE2E(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	t.Run("non-admin session is forbidden", func(t *testing.T) {
		token, _, _ := loginE2ENonAdminSession(t, client, fixtures)
		resp := client.AdminGET(t, token)
		if resp.status != http.StatusForbidden {
			t.Errorf("expected 403, got %d: %s", resp.status, resp.raw)
		}
	})

	t.Run("admin session is allowed", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true, Admin: true})
		lr := client.MustLogin(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
		resp := client.AdminGET(t, lr.Token)
		if resp.status != http.StatusOK {
			t.Errorf("expected 200, got %d: %s", resp.status, resp.raw)
		}
	})

	t.Run("missing token is unauthorized", func(t *testing.T) {
		resp := client.AdminGET(t, "")
		if resp.status != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d: %s", resp.status, resp.raw)
		}
	})

	t.Run("expired token is unauthorized", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: true, Admin: true})
		lr := client.MustLogin(t, employee.Username, employee.Password, loginE2EFarLat, loginE2EFarLong, "")
		fixtures.SetSessionExpiresAt(t, lr.Token, time.Now().Add(-time.Minute))

		resp := client.AdminGET(t, lr.Token)
		if resp.status != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d: %s", resp.status, resp.raw)
		}
	})
}

// TestActivationThenLoginE2E covers cases 34-38: a not-yet-activated
// Employee is turned away by Login, completes POST /v1/activate, and then
// logs in for real — plus the activation token's own reuse/expiry edges.
func TestActivationThenLoginE2E(t *testing.T) {
	client, fixtures := loginE2ESetup(t)

	t.Run("not activated, then activate, then login succeeds", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: false})

		beforeActivation := client.Login(t, employee.Username, "whatever-password", loginE2EFarLat, loginE2EFarLong, "")
		if beforeActivation.status != http.StatusForbidden {
			t.Fatalf("login before activation: expected 403 (ErrAccountNotActivated), got %d: %s", beforeActivation.status, beforeActivation.raw)
		}

		token := fixtures.ActivationToken(t, employee.ID, time.Now().Add(time.Hour))
		const newPassword = "brand-new-password-123"

		client.MustActivate(t, token, newPassword)

		storeID := fixtures.Store(t, storeSeed{IPs: []string{loginE2ELoopbackIP}})
		fixtures.Link(t, employee.ID, storeID)

		lr := client.MustLogin(t, employee.Username, newPassword, loginE2EFarLat, loginE2EFarLong, "")
		if lr.StoreID == nil || *lr.StoreID != storeID {
			t.Errorf("expected the first real login to match store_id=%d, got %v", storeID, lr.StoreID)
		}

		reuse := client.Activate(t, token, "another-password-456")
		if reuse.status != http.StatusBadRequest {
			t.Errorf("reusing an already-redeemed activation token: expected 400, got %d: %s", reuse.status, reuse.raw)
		}
	})

	t.Run("expired activation token is rejected", func(t *testing.T) {
		employee := fixtures.Employee(t, employeeSeed{Activated: false})
		token := fixtures.ActivationToken(t, employee.ID, time.Now().Add(-time.Hour))

		resp := client.Activate(t, token, "irrelevant-password")
		if resp.status != http.StatusBadRequest {
			t.Errorf("expected 400 (ErrInvalidOrExpiredToken), got %d: %s", resp.status, resp.raw)
		}
	})
}
