package auth

import (
	"context"
	"errors"
	"math"
	"net"
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
	// non-Admin Session it creates, regardless of Heartbeat outcome — the
	// backstop against a forgotten Logout (ADR-0014).
	nonAdminSessionTTL = 12 * time.Hour

	// adminSessionTTL is the flat expiry Login applies to an Admin Session
	// (issue #24) — no presence check to re-verify, so no Heartbeat, so no
	// absolute-cap-vs-heartbeat-outcome distinction to make: just one flat
	// duration (ADR-0014/ADR-0015).
	adminSessionTTL = 8 * time.Hour

	// heartbeatFailureThreshold is the count RecordHeartbeatFailure's
	// returned consecutive_failures must reach before Heartbeat ends the
	// Session — "two consecutive failed heartbeats" (ADR-0014, issue #23).
	heartbeatFailureThreshold = 2

	// heartbeatSilenceTimeout is the backstop against a device that's gone
	// fully silent (killed app, dead battery, no connectivity): no
	// heartbeat at all — pass or fail — for this long also ends the
	// Session (ADR-0014, issue #23).
	heartbeatSilenceTimeout = 90 * time.Second

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

	// notActivated is an Employee who exists but has never completed
	// POST /v1/activate — password is still NULL/empty, so there is no
	// real hash to compare against.
	notActivated := found && len(employee.Password) == 0

	// The bcrypt compare always runs — against dummyPasswordHash whenever
	// there's no real password hash to check (unknown username or a
	// not-yet-activated Employee) — so those cases, a deactivated or
	// locked-out Employee, and a genuinely wrong password all cost the
	// same, before any of those cases short-circuits below. Skipping this
	// call whenever found/IsActive/locked already told us the answer would
	// make Login measurably faster for those cases than for a real wrong
	// password, letting a timing attack distinguish states
	// ErrInvalidCredentials deliberately doesn't.
	passwordHash := []byte(dummyPasswordHash)
	if found && !notActivated {
		passwordHash = employee.Password
	}
	passwordMatches := bcrypt.CompareHashAndPassword(passwordHash, []byte(params.Password)) == nil

	if !found {
		return LoginResult{}, ErrInvalidCredentials
	}
	// Distinct from ErrInvalidCredentials — the Employee hasn't done
	// anything wrong, they just haven't finished activation yet.
	if notActivated {
		return LoginResult{}, ErrAccountNotActivated
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

	admin, err := s.isAdmin(ctx, employee.ID)
	if err != nil {
		return LoginResult{}, err
	}

	// storeID stays the zero value (Valid: false) for an Admin Session —
	// Admin Login skips the presence check entirely, so there is no Store
	// to record (ADR-0015).
	var storeID pgtype.Int8
	ttl := nonAdminSessionTTL
	if admin {
		ttl = adminSessionTTL
	} else {
		candidates, err := s.repo.ListStoresForLoginByEmployeeID(ctx, employee.ID)
		if err != nil {
			return LoginResult{}, err
		}
		store, ok := matchStore(candidates, clientIP, *params.Latitude, *params.Longitude, parseMAC(params.MACAddress))
		if !ok {
			return LoginResult{}, ErrNoStoreMatch
		}
		storeID = pgtype.Int8{Int64: store.ID, Valid: true}
	}

	token, err := tokenx.Generate(sessionTokenBytes)
	if err != nil {
		return LoginResult{}, err
	}

	session, err := s.repo.UpsertSession(ctx, repo.UpsertSessionParams{
		EmployeeID: employee.ID,
		StoreID:    storeID,
		TokenHash:  tokenx.Hash(token),
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(ttl), Valid: true},
	})
	if err != nil {
		return LoginResult{}, err
	}

	return LoginResult{Token: token, Session: session}, nil
}

// isAdmin is the one call site (ADR-0015) for "does this Employee hold the
// Admin Position" — Login's presence-check bypass (issue #24) and
// ValidateSession's admin-gating check (issue #25) both go through this
// rather than duplicating the position-name comparison.
func (s *service) isAdmin(ctx context.Context, employeeID int64) (bool, error) {
	return s.repo.IsEmployeeAdmin(ctx, employeeID)
}

// Logout deletes the Session matching token's hash — see Service.Logout for
// why a no-op delete (0 rows) is not an error.
func (s *service) Logout(ctx context.Context, token string) error {
	_, err := s.repo.DeleteSessionByTokenHash(ctx, tokenx.Hash(token))
	return err
}

// Heartbeat reruns Login's presence check against token's Session's Store
// (ADR-0014, issue #23). See GetSessionByTokenHash's comment for why a token
// this query can't resolve to an open Session always reports
// ReasonLoggedOutElsewhere, regardless of the actual cause.
func (s *service) Heartbeat(ctx context.Context, token string, params heartbeatParams, clientIP netip.Addr) (HeartbeatResult, error) {
	tokenHash := tokenx.Hash(token)

	session, err := s.repo.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return HeartbeatResult{Reason: ReasonLoggedOutElsewhere}, nil
		}
		return HeartbeatResult{}, err
	}

	// An Admin Session carries no Store and is never Heartbeat-monitored
	// (ADR-0015) — a no-op that neither ends the Session nor touches its
	// heartbeat bookkeeping.
	if !session.StoreID.Valid {
		return HeartbeatResult{Active: true}, nil
	}

	now := time.Now()
	silent := now.Sub(session.LastHeartbeatAt.Time) > heartbeatSilenceTimeout
	if session.ExpiresAt.Time.Before(now) || silent {
		if _, err := s.repo.DeleteSessionByTokenHash(ctx, tokenHash); err != nil {
			return HeartbeatResult{}, err
		}
		return HeartbeatResult{Reason: ReasonSessionExpired}, nil
	}

	store, err := s.repo.GetStoreByID(ctx, session.StoreID.Int64)
	if err != nil {
		return HeartbeatResult{}, err
	}
	ips, err := s.repo.ListStoreWifiIPsByStoreID(ctx, store.ID)
	if err != nil {
		return HeartbeatResult{}, err
	}
	macs, err := s.repo.ListStoreWifiMacsByStoreID(ctx, store.ID)
	if err != nil {
		return HeartbeatResult{}, err
	}
	candidate := repo.ListStoresForLoginByEmployeeIDRow{Store: store, IpAddresses: ips, MacAddresses: macs}

	_, matched := matchStore([]repo.ListStoresForLoginByEmployeeIDRow{candidate}, clientIP, *params.Latitude, *params.Longitude, parseMAC(params.MACAddress))
	if matched {
		if _, err := s.repo.RecordHeartbeatSuccess(ctx, tokenHash); err != nil {
			return HeartbeatResult{}, err
		}
		return HeartbeatResult{Active: true}, nil
	}

	failed, err := s.repo.RecordHeartbeatFailure(ctx, tokenHash)
	if err != nil {
		return HeartbeatResult{}, err
	}
	if failed.ConsecutiveFailures >= heartbeatFailureThreshold {
		if _, err := s.repo.DeleteSessionByTokenHash(ctx, tokenHash); err != nil {
			return HeartbeatResult{}, err
		}
		return HeartbeatResult{Reason: ReasonLeftPremises}, nil
	}
	return HeartbeatResult{Active: true}, nil
}

// ValidateSession resolves token to the Session it names (issue #25) — the
// one seam the admin-gating middleware, and any future authenticated
// handler, hangs off. ErrSessionNotFound covers both "no such token" and
// "found, but past its expiry" identically; the caller only needs to know
// the Session isn't currently open, not why.
func (s *service) ValidateSession(ctx context.Context, token string) (ValidatedSession, error) {
	session, err := s.repo.GetSessionByTokenHash(ctx, tokenx.Hash(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ValidatedSession{}, ErrSessionNotFound
		}
		return ValidatedSession{}, err
	}
	if session.ExpiresAt.Time.Before(time.Now()) {
		return ValidatedSession{}, ErrSessionNotFound
	}

	admin, err := s.isAdmin(ctx, session.EmployeeID)
	if err != nil {
		return ValidatedSession{}, err
	}
	return ValidatedSession{EmployeeID: session.EmployeeID, IsAdmin: admin}, nil
}

// matchStore implements Login's/Heartbeat's presence check (ADR-0013) over
// one Employee's candidate Stores — already ordered by store id
// (ListStoresForLoginByEmployeeID) for a deterministic tie-break when more
// than one Store could plausibly match. IP is tried first, across every
// candidate, then Geofence, then MAC last across every candidate again —
// "trust-ordered, not request-ordered" per ADR-0013's title. mac is nil
// when the caller (native-app-only, best-effort) didn't submit one; the MAC
// tier is then simply never checked, same as an empty candidate list would
// be.
func matchStore(candidates []repo.ListStoresForLoginByEmployeeIDRow, clientIP netip.Addr, latitude, longitude float64, mac net.HardwareAddr) (repo.Store, bool) {
	for _, c := range candidates {
		// wifi_whitelist_enabled gates only the Wifi Whitelist tiers
		// (CONTEXT.md's Store entry, both IP and MAC) — it has no bearing
		// on the Geofence pass below.
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

	if mac != nil {
		for _, c := range candidates {
			if !c.Store.WifiWhitelistEnabled {
				continue
			}
			for _, candidateMAC := range c.MacAddresses {
				if bytesEqualMAC(candidateMAC, mac) {
					return c.Store, true
				}
			}
		}
	}

	return repo.Store{}, false
}

// bytesEqualMAC compares two net.HardwareAddr byte-for-byte — net.ParseMAC
// (both here and in the Wifi Whitelist's own store_wifi_mac writes, see
// stores.parseAddresses) always normalizes to 6 raw bytes, so a direct
// byte-slice comparison is exact, not a string-format coincidence.
func bytesEqualMAC(a, b net.HardwareAddr) bool {
	return slices.Equal(a, b)
}

// parseMAC returns the parsed form of a device's optional, already-validated
// (validate:"omitempty,mac") MAC/BSSID string, or nil for an omitted one —
// matchStore treats a nil mac as "don't check this tier" rather than an
// error, since a malformed value can never reach here past validation.
func parseMAC(raw string) net.HardwareAddr {
	if raw == "" {
		return nil
	}
	mac, err := net.ParseMAC(raw)
	if err != nil {
		return nil
	}
	return mac
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
