//go:build dbe2e

// loginE2EFixtures is the one seam every test in cmd/login_e2e_test.go
// crosses to touch Postgres directly: creating new Employee/Store rows
// (Employee, Store, Link, ActivationToken), and mutating/inspecting
// existing ones past the HTTP API (Set*/the query methods) — the time-travel
// primitives lockout expiry, session TTL, and the silence backstop rely on,
// plus the DB-state assertions Heartbeat's tests need. Reaching past the API
// here is deliberate, the same way ADR-0015's Admin bootstrap script does —
// this suite's fixtures aren't what's under test, so no query exists (or
// should exist) on the real API for most of what this module does.
package main

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/tokenx"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type loginE2EFixtures struct {
	pool *pgxpool.Pool
	repo repo.Querier
}

func newLoginE2EFixtures(pool *pgxpool.Pool, q repo.Querier) *loginE2EFixtures {
	return &loginE2EFixtures{pool: pool, repo: q}
}

// loginE2EEmployee is Employee's result: enough for a test to log in as the
// Employee it just seeded, or to Link it to a Store.
type loginE2EEmployee struct {
	ID       int64
	Username string
	Password string
}

// employeeSeed configures Employee's fixture. The zero value is a
// never-activated Employee (password stays NULL, exactly like a real
// admin-created-but-not-yet-activated row).
type employeeSeed struct {
	Activated bool
	// Password overrides the plaintext password hashed into the row; only
	// meaningful when Activated is true. Defaults to
	// loginE2EDefaultPassword.
	Password            string
	Inactive            bool
	FailedLoginAttempts int32
	LockedUntil         *time.Time
	Admin               bool
}

// Employee inserts an Employee row via the real CreateEmployee query
// (exercising its own uniqueness constraints), then sets whatever
// activation/lockout/admin state seed asks for directly — no query exists
// (or should exist) for an admin to set failed_login_attempts/locked_until
// via the HTTP API, so this reaches past it the same way ADR-0015's
// bootstrap script reaches past the API for the first Admin.
func (f *loginE2EFixtures) Employee(t *testing.T, seed employeeSeed) loginE2EEmployee {
	t.Helper()
	ctx := t.Context()

	username := e2eUnique(t, "login")
	employee, err := f.repo.CreateEmployee(ctx, repo.CreateEmployeeParams{
		OdooEmployeeID: loginE2ENextOdooID(),
		FullName:       "Login E2E " + username,
		Email:          username + "@example.com",
		Username:       username,
	})
	if err != nil {
		t.Fatalf("seed employee: CreateEmployee: %v", err)
	}
	t.Cleanup(func() {
		if _, err := f.repo.DeleteEmployee(context.Background(), employee.ID); err != nil {
			t.Errorf("cleanup: delete employee %d: %v", employee.ID, err)
		}
	})

	var password string
	var hashed []byte
	if seed.Activated {
		password = seed.Password
		if password == "" {
			password = loginE2EDefaultPassword
		}
		hashed, err = bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			t.Fatalf("seed employee: hash password: %v", err)
		}
	}

	var lockedUntil pgtype.Timestamptz
	if seed.LockedUntil != nil {
		lockedUntil = pgtype.Timestamptz{Time: *seed.LockedUntil, Valid: true}
	}

	if _, err := f.pool.Exec(ctx, `
		UPDATE employees
		SET password = $2, is_active = $3, failed_login_attempts = $4, locked_until = $5
		WHERE id = $1
	`, employee.ID, hashed, !seed.Inactive, seed.FailedLoginAttempts, lockedUntil); err != nil {
		t.Fatalf("seed employee: set state: %v", err)
	}

	if seed.Admin {
		adminID := f.ensureAdminPositionID(t)
		if err := f.repo.InsertEmployeePositions(ctx, repo.InsertEmployeePositionsParams{
			EmployeeID:  employee.ID,
			PositionIds: []int64{adminID},
		}); err != nil {
			t.Fatalf("seed employee: grant Admin: %v", err)
		}
	}

	return loginE2EEmployee{ID: employee.ID, Username: username, Password: password}
}

// ensureAdminPositionID idempotently creates the "Admin" Position ADR-0015
// gates on (positions.name is UNIQUE, so ON CONFLICT DO NOTHING makes this
// safe to call from every test, including in parallel) and returns its id.
// The row itself is never cleaned up — it's a shared, reusable fixture, the
// same way a real environment's bootstrap-created Admin Position persists.
func (f *loginE2EFixtures) ensureAdminPositionID(t *testing.T) int64 {
	t.Helper()

	if _, err := f.pool.Exec(t.Context(), `INSERT INTO positions (name) VALUES ('Admin') ON CONFLICT (name) DO NOTHING`); err != nil {
		t.Fatalf("ensure Admin position: %v", err)
	}
	var id int64
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM positions WHERE name = 'Admin'`).Scan(&id); err != nil {
		t.Fatalf("ensure Admin position: lookup id: %v", err)
	}
	return id
}

// storeSeed configures Store's fixture. The zero value is a
// wifi-whitelist-enabled Store with no IP/MAC entries and no geofence —
// matches nothing on any tier.
type storeSeed struct {
	WifiWhitelistDisabled bool
	IPs                   []string
	MACs                  []string
	Latitude, Longitude   *float64
	RadiusMeters          *int32
}

// Store inserts a bare Store row directly (no sqlc query creates a plain
// local Store — UpsertStores is Odoo-sync-only, see ADR-0009), then layers
// on geofence/wifi-whitelist state through the same sqlc queries
// PATCH /v1/stores/{id} itself uses, so those columns' actual pgx type
// handling (numeric, inet, macaddr) is never hand-rolled here.
func (f *loginE2EFixtures) Store(t *testing.T, seed storeSeed) int64 {
	t.Helper()
	ctx := t.Context()

	var id int64
	var updatedAt pgtype.Timestamptz
	name := e2eUnique(t, "store")
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO store (store_name, wifi_whitelist_enabled)
		VALUES ($1, $2)
		RETURNING id, updated_at
	`, name, !seed.WifiWhitelistDisabled).Scan(&id, &updatedAt); err != nil {
		t.Fatalf("seed store: insert: %v", err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `DELETE FROM store WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup: delete store %d: %v", id, err)
		}
	})

	if seed.Latitude != nil || seed.Longitude != nil || seed.RadiusMeters != nil {
		lat, err := loginE2EFloatToNumeric(seed.Latitude)
		if err != nil {
			t.Fatalf("seed store: latitude: %v", err)
		}
		long, err := loginE2EFloatToNumeric(seed.Longitude)
		if err != nil {
			t.Fatalf("seed store: longitude: %v", err)
		}
		var radius pgtype.Int4
		if seed.RadiusMeters != nil {
			radius = pgtype.Int4{Int32: *seed.RadiusMeters, Valid: true}
		}
		if _, err := f.repo.UpdateStoreGeofence(ctx, repo.UpdateStoreGeofenceParams{
			Latitude:          lat,
			Longitude:         long,
			RadiusMeters:      radius,
			ID:                id,
			ExpectedUpdatedAt: updatedAt,
		}); err != nil {
			t.Fatalf("seed store: set geofence: %v", err)
		}
	}

	if len(seed.IPs) > 0 {
		ips := make([]netip.Addr, len(seed.IPs))
		for i, s := range seed.IPs {
			ips[i] = netip.MustParseAddr(s)
		}
		if err := f.repo.InsertStoreWifiIPs(ctx, repo.InsertStoreWifiIPsParams{StoreID: id, IpAddresses: ips}); err != nil {
			t.Fatalf("seed store: insert wifi IPs: %v", err)
		}
	}
	if len(seed.MACs) > 0 {
		macs := make([]net.HardwareAddr, len(seed.MACs))
		for i, s := range seed.MACs {
			mac, err := net.ParseMAC(s)
			if err != nil {
				t.Fatalf("seed store: parse MAC %q: %v", s, err)
			}
			macs[i] = mac
		}
		if err := f.repo.InsertStoreWifiMacs(ctx, repo.InsertStoreWifiMacsParams{StoreID: id, MacAddresses: macs}); err != nil {
			t.Fatalf("seed store: insert wifi MACs: %v", err)
		}
	}

	return id
}

// loginE2EFloatToNumeric mirrors stores.float64PtrToNumeric (unexported,
// different package) — pgtype.Numeric.Scan only accepts a string or nil,
// and 'f' formatting avoids scientific notation, which Scan can't parse
// back.
func loginE2EFloatToNumeric(f *float64) (pgtype.Numeric, error) {
	if f == nil {
		return pgtype.Numeric{}, nil
	}
	var n pgtype.Numeric
	if err := n.Scan(strconv.FormatFloat(*f, 'f', -1, 64)); err != nil {
		return pgtype.Numeric{}, err
	}
	return n, nil
}

// Link attaches employeeID to storeID (employee_stores), the membership
// Login's presence check (ADR-0013) reads candidate Stores from.
func (f *loginE2EFixtures) Link(t *testing.T, employeeID, storeID int64) {
	t.Helper()
	if err := f.repo.InsertEmployeeStores(t.Context(), repo.InsertEmployeeStoresParams{
		EmployeeID: employeeID,
		StoreIds:   []int64{storeID},
	}); err != nil {
		t.Fatalf("link employee %d to store %d: %v", employeeID, storeID, err)
	}
}

// ActivationToken creates a password_reset_tokens row directly (same table
// CompleteActivation redeems, see internal/employees/service.go) with a
// known raw bearer token, bypassing the admin-triggered send-link/mailer
// flow entirely — this suite cares whether Activate-then-Login works, not
// whether the email got sent.
func (f *loginE2EFixtures) ActivationToken(t *testing.T, employeeID int64, expiresAt time.Time) string {
	t.Helper()

	raw, err := tokenx.Generate(32)
	if err != nil {
		t.Fatalf("seed activation token: generate: %v", err)
	}
	if _, err := f.repo.CreatePasswordResetToken(t.Context(), repo.CreatePasswordResetTokenParams{
		EmployeeID: employeeID,
		TokenHash:  tokenx.Hash(raw),
		ExpiresAt:  pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		t.Fatalf("seed activation token: create: %v", err)
	}
	return raw
}

// SetLockedUntil overwrites an already-seeded Employee's locked_until
// directly — the time-travel primitive TestLoginE2E_Lockout uses to
// simulate a lockout window having passed, without an actual 15-minute
// wait.
func (f *loginE2EFixtures) SetLockedUntil(t *testing.T, employeeID int64, when time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE employees SET locked_until = $2 WHERE id = $1`, employeeID, when); err != nil {
		t.Fatalf("set employee %d locked_until: %v", employeeID, err)
	}
}

// SetSessionExpiresAt overwrites an already-issued Session's expires_at
// directly — the time-travel primitive Heartbeat's absolute-TTL-expiry case
// uses, without an actual 12-hour/8-hour wait.
func (f *loginE2EFixtures) SetSessionExpiresAt(t *testing.T, token string, when time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET expires_at = $2 WHERE token_hash = $1`, tokenx.Hash(token), when); err != nil {
		t.Fatalf("set session expires_at: %v", err)
	}
}

// SetSessionLastHeartbeatAt overwrites an already-issued Session's
// last_heartbeat_at directly — the time-travel primitive Heartbeat's 90s
// silence-backstop case uses, without an actual wait.
func (f *loginE2EFixtures) SetSessionLastHeartbeatAt(t *testing.T, token string, when time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET last_heartbeat_at = $2 WHERE token_hash = $1`, tokenx.Hash(token), when); err != nil {
		t.Fatalf("set session last_heartbeat_at: %v", err)
	}
}

// loginE2ELockState is EmployeeLockState's result.
type loginE2ELockState struct {
	FailedAttempts int32
	LockedUntil    pgtype.Timestamptz
}

// EmployeeLockState reads an Employee's lockout counters directly — what
// TestLoginE2E_Lockout/TestLoginE2E_ResetsFailedAttempts assert against.
func (f *loginE2EFixtures) EmployeeLockState(t *testing.T, employeeID int64) loginE2ELockState {
	t.Helper()
	var s loginE2ELockState
	if err := f.pool.QueryRow(t.Context(), `SELECT failed_login_attempts, locked_until FROM employees WHERE id = $1`, employeeID).Scan(&s.FailedAttempts, &s.LockedUntil); err != nil {
		t.Fatalf("query employee %d lock state: %v", employeeID, err)
	}
	return s
}

// SessionConsecutiveFailures reads a Session's Heartbeat failure counter
// directly — what the failure/reset Heartbeat cases assert against.
func (f *loginE2EFixtures) SessionConsecutiveFailures(t *testing.T, token string) int32 {
	t.Helper()
	var n int32
	if err := f.pool.QueryRow(t.Context(), `SELECT consecutive_failures FROM sessions WHERE token_hash = $1`, tokenx.Hash(token)).Scan(&n); err != nil {
		t.Fatalf("query session consecutive_failures: %v", err)
	}
	return n
}

// SessionExists reports whether a Session row still exists for token — what
// every "expect the Session ended" assertion in this suite checks.
func (f *loginE2EFixtures) SessionExists(t *testing.T, token string) bool {
	t.Helper()
	var exists bool
	if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM sessions WHERE token_hash = $1)`, tokenx.Hash(token)).Scan(&exists); err != nil {
		t.Fatalf("check session existence for token: %v", err)
	}
	return exists
}

// loginE2EOdooIDCounter hands out unique odoo_employee_id values (that
// column is NOT NULL UNIQUE, but otherwise meaningless to Login) — seeded
// from the current time so re-runs across process restarts don't collide
// with rows a prior run failed to clean up.
var loginE2EOdooIDCounter = time.Now().UnixNano()

func loginE2ENextOdooID() int64 {
	return atomic.AddInt64(&loginE2EOdooIDCounter, 1)
}

func floatPtr(f float64) *float64    { return &f }
func int32Ptr(i int32) *int32        { return &i }
func timePtr(t time.Time) *time.Time { return &t }
