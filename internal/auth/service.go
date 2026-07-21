package auth

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"slices"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/tokenx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	// loginLockoutThreshold and loginLockoutDuration implement "5
	// consecutive failed password attempts locks that Employee's login for
	// 15 minutes" (issue #21) — tracked per-Employee via
	// employees.failed_login_attempts/locked_until, not per-IP.
	loginLockoutThreshold = 5
	loginLockoutDuration  = 15 * time.Minute

	// nonAdminSessionTTL is the absolute expiry cap Login applies to every
	// Session it creates, regardless of Heartbeat outcome — the backstop
	// against a forgotten Logout (ADR-0014). Admin Sessions' flat 8-hour
	// expiry, and Admin Login's presence-check bypass, are out of this
	// ticket's scope — see issue #24.
	nonAdminSessionTTL = 12 * time.Hour

	// sessionTokenBytes is the raw bearer token's length before hex
	// encoding — same size as employees' activationTokenBytes.
	sessionTokenBytes = 32

	// earthRadiusMeters is the mean Earth radius haversineMeters uses —
	// accurate enough for a store-radius geofence check, not survey-grade
	// geodesy.
	earthRadiusMeters = 6371000.0

	// dummyPasswordHash is a valid bcrypt hash of an arbitrary, unused
	// password — compared against on an unknown username so Login spends
	// the same bcrypt CPU cost whether or not the username exists.
	// Without this, ErrInvalidCredentials for "no such username" would
	// return measurably faster than for "wrong password" (which pays
	// bcrypt.CompareHashAndPassword's ~tens-of-milliseconds cost), letting
	// a timing attack distinguish the two cases the response body and
	// status code deliberately don't.
	dummyPasswordHash = "$2a$10$e/E6OXsCT4Mx01Ynej7bWe5lZ2oDsC7fma49mWr2AfwiQRShXNmkq"
)

type service struct {
	// repo is the only Querier Login/Logout need. Unlike employees/stores,
	// Login has no step that needs transaction scoping: every write here
	// (RecordFailedLoginAttempt, ResetFailedLoginAttempts, UpsertSession)
	// is already a single atomic statement, and — critically — a wrong
	// password's RecordFailedLoginAttempt write must survive even though
	// Login goes on to return ErrInvalidCredentials; wrapping the whole
	// method in a withTx-style "any returned error rolls back" transaction
	// (the employees/stores convention) would silently discard that write
	// on every failed attempt, breaking the lockout counter entirely.
	repo repo.Querier
}

func NewService(pool *pgxpool.Pool) Service {
	return &service{repo: repo.New(pool)}
}

// Login verifies params.Username/Password, then verifies presence at one of
// the Employee's Stores via matchStore (ADR-0013), and — only if both check
// out — issues a new Session, superseding any Session already open for that
// Employee (ADR-0014, UpsertSession's ON CONFLICT).
func (s *service) Login(ctx context.Context, params loginParams, clientIP netip.Addr) (LoginResult, error) {
	employee, err := s.repo.GetEmployeeByUsername(ctx, params.Username)
	found := true
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return LoginResult{}, err
		}
		found = false
	}

	// The bcrypt compare always runs — against dummyPasswordHash when no
	// such Employee exists — so an unknown username, a deactivated or
	// locked-out Employee, and a genuinely wrong password all cost the
	// same, before any of those cases short-circuits below. Skipping this
	// call whenever found/IsActive/locked already told us the answer would
	// make Login measurably faster for those cases than for a real wrong
	// password, letting a timing attack distinguish states
	// ErrInvalidCredentials deliberately doesn't.
	passwordHash := []byte(dummyPasswordHash)
	if found {
		passwordHash = employee.Password
	}
	passwordMatches := bcrypt.CompareHashAndPassword(passwordHash, []byte(params.Password)) == nil

	if !found {
		return LoginResult{}, ErrInvalidCredentials
	}
	// Folded into the same generic error as a wrong password — see
	// ErrInvalidCredentials.
	if !employee.IsActive {
		return LoginResult{}, ErrInvalidCredentials
	}
	if employee.LockedUntil.Valid && employee.LockedUntil.Time.After(time.Now()) {
		return LoginResult{}, ErrInvalidCredentials
	}

	if !passwordMatches {
		if _, recordErr := s.repo.RecordFailedLoginAttempt(ctx, repo.RecordFailedLoginAttemptParams{
			ID:          employee.ID,
			Threshold:   loginLockoutThreshold,
			LockedUntil: pgtype.Timestamptz{Time: time.Now().Add(loginLockoutDuration), Valid: true},
		}); recordErr != nil {
			return LoginResult{}, recordErr
		}
		return LoginResult{}, ErrInvalidCredentials
	}

	if err := s.repo.ResetFailedLoginAttempts(ctx, employee.ID); err != nil {
		return LoginResult{}, err
	}

	candidates, err := s.repo.ListStoresForLoginByEmployeeID(ctx, employee.ID)
	if err != nil {
		return LoginResult{}, err
	}
	store, ok := matchStore(candidates, clientIP, *params.Latitude, *params.Longitude)
	if !ok {
		return LoginResult{}, ErrNoStoreMatch
	}

	token, err := tokenx.Generate(sessionTokenBytes)
	if err != nil {
		return LoginResult{}, err
	}

	session, err := s.repo.UpsertSession(ctx, repo.UpsertSessionParams{
		EmployeeID: employee.ID,
		StoreID:    pgtype.Int8{Int64: store.ID, Valid: true},
		TokenHash:  tokenx.Hash(token),
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(nonAdminSessionTTL), Valid: true},
	})
	if err != nil {
		return LoginResult{}, err
	}

	return LoginResult{Token: token, Session: session}, nil
}

// Logout deletes the Session matching token's hash — see Service.Logout for
// why a no-op delete (0 rows) is not an error.
func (s *service) Logout(ctx context.Context, token string) error {
	_, err := s.repo.DeleteSessionByTokenHash(ctx, tokenx.Hash(token))
	return err
}

// matchStore implements Login's presence check (ADR-0013) over one
// Employee's candidate Stores — already ordered by store id
// (ListStoresForLoginByEmployeeID) for a deterministic tie-break when more
// than one Store could plausibly match. IP is tried first, across every
// candidate, before Geofence is tried at all — "trust-ordered, not
// request-ordered" per ADR-0013's title. MAC (ADR-0013's third tier) is out
// of this ticket's scope — see issue #22.
func matchStore(candidates []repo.ListStoresForLoginByEmployeeIDRow, clientIP netip.Addr, latitude, longitude float64) (repo.Store, bool) {
	for _, c := range candidates {
		// wifi_whitelist_enabled gates only this tier (CONTEXT.md's Store
		// entry) — it has no bearing on the Geofence pass below.
		if !c.Store.WifiWhitelistEnabled {
			continue
		}
		if slices.Contains(c.IpAddresses, clientIP) {
			return c.Store, true
		}
	}

	for _, c := range candidates {
		store := c.Store
		if !store.Latitude.Valid || !store.Longitude.Valid || !store.RadiusMeters.Valid {
			continue
		}
		storeLat, err := store.Latitude.Float64Value()
		if err != nil || !storeLat.Valid {
			continue
		}
		storeLong, err := store.Longitude.Float64Value()
		if err != nil || !storeLong.Valid {
			continue
		}
		if haversineMeters(latitude, longitude, storeLat.Float64, storeLong.Float64) <= float64(store.RadiusMeters.Int32) {
			return store, true
		}
	}

	return repo.Store{}, false
}

// haversineMeters returns the great-circle distance, in meters, between two
// latitude/longitude points.
func haversineMeters(lat1, long1, lat2, long2 float64) float64 {
	lat1Rad, lat2Rad := lat1*math.Pi/180, lat2*math.Pi/180
	dLat := (lat2 - lat1) * math.Pi / 180
	dLong := (long2 - long1) * math.Pi / 180

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Sin(dLong/2)*math.Sin(dLong/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return earthRadiusMeters * c
}
