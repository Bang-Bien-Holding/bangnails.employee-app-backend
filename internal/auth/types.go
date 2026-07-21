package auth

//go:generate mockgen -source=types.go -destination=service_mock_test.go -package=auth

import (
	"context"
	"errors"
	"net/netip"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrInvalidCredentials is returned by Login for a wrong username, a wrong
// password, a locked-out Employee, or a deactivated one — deliberately one
// generic error rather than four distinct ones. Acceptance criteria only
// requires collapsing wrong-username and wrong-password (see issue #21),
// but locked-out and deactivated get folded in too: telling an unauthorized
// caller "this account is locked" or "this account is deactivated" leaks
// account state to someone who, by definition, hasn't proven they're the
// account's owner yet.
var ErrInvalidCredentials = errors.New("invalid username or password")

// ErrAccountNotActivated is returned by Login for an Employee who exists but
// has never completed the activation flow (POST /v1/activate) — password is
// still NULL, so there is no password to check against at all. Deliberately
// distinct from ErrInvalidCredentials: the Employee has done nothing wrong
// here, they just haven't finished setup yet, so the caller is told to
// activate rather than being told their credentials are wrong.
var ErrAccountNotActivated = errors.New("please activate your account")

// ErrNoStoreMatch is returned by Login when username/password check out but
// none of the Employee's Stores match on IP, Geofence, or MAC (ADR-0013) — a
// distinct, non-generic error, unlike ErrInvalidCredentials, since the
// caller has already proven who they are; what's missing is presence, not
// identity.
var ErrNoStoreMatch = errors.New("not at an authorized store")

// ErrSessionNotFound is returned by ValidateSession for a bearer token that
// names no currently-open Session — never valid, already logged out, or
// expired. The admin-gating middleware (issue #25) maps it to 401.
var ErrSessionNotFound = errors.New("session not found or expired")

// devicePresenceParams is the presence-check input shared by Login and
// Heartbeat (ADR-0013): GPS coordinates plus the optional, native-app-only
// MAC/BSSID of the currently-connected Wi-Fi network. Latitude/Longitude are
// pointers, not plain float64s, so an omitted field ("required" on a
// pointer catches nil) is distinguishable from an explicit 0 — a real point
// on the equator/prime meridian, not a sentinel for "not sent" (same
// convention as stores.patchStoreParams' geofence fields). MACAddress needs
// no pointer for the same "omitted vs. sent" reason: an empty string is
// never a valid MAC, so it's already unambiguous (same convention as
// stores.patchStoreParams' address slices). The server-observed request IP
// is deliberately not a field here — ADR-0013: "the app sends no IP field
// for this check" — Handler supplies it separately, read from the
// connection itself.
type devicePresenceParams struct {
	Latitude   *float64 `json:"latitude" validate:"required,min=-90,max=90"`
	Longitude  *float64 `json:"longitude" validate:"required,min=-180,max=180"`
	MACAddress string   `json:"mac_address" validate:"omitempty,mac"`
}

// loginParams is the body for POST /v1/auth/login.
type loginParams struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
	devicePresenceParams
}

// heartbeatParams is the body for POST /v1/auth/heartbeat — the same
// presence-check inputs Login takes, re-submitted every ~30s while a
// non-Admin Session is open (ADR-0014). The session token itself travels in
// the Authorization header, same as Logout, not in this body.
type heartbeatParams struct {
	devicePresenceParams
}

// LoginResult is Login's success return: the raw bearer token (which, like
// a password-reset token, never touches the database — only its SHA-256
// digest does, see hashToken) alongside the persisted Session row it now
// names.
type LoginResult struct {
	Token   string
	Session repo.Session
}

// Heartbeat's forced-session-end reason codes (ADR-0014) — carried in
// HeartbeatResult so the app can tell the Employee why, instead of a bare
// "please log in again".
const (
	// ReasonLeftPremises means two consecutive presence rechecks failed.
	ReasonLeftPremises = "left_premises"
	// ReasonSessionExpired means the Session's absolute expiry passed, or
	// no heartbeat (pass or fail) arrived for the 90s silence backstop.
	ReasonSessionExpired = "session_expired"
	// ReasonLoggedOutElsewhere means the submitted token no longer names an
	// open Session — most commonly because a newer Login superseded it
	// (ADR-0014's single-active-Session rule), but also the generic answer
	// for any other token this query can't find (see
	// repo.Querier.GetSessionByTokenHash's comment) — this ticket doesn't
	// need to tell those cases apart from Heartbeat's caller's perspective.
	ReasonLoggedOutElsewhere = "logged_out_elsewhere"
)

// HeartbeatResult is Heartbeat's return: Active reports whether the Session
// is still open after this check; Reason is one of the Reason* constants
// above, set only when Active is false.
type HeartbeatResult struct {
	Active bool
	Reason string
}

// ValidatedSession is ValidateSession's return — just enough for the
// admin-gating middleware (issue #25) to decide access: which Employee the
// token belongs to, and whether they hold the Admin Position.
type ValidatedSession struct {
	EmployeeID int64
	IsAdmin    bool
}

type Service interface {
	Login(ctx context.Context, params loginParams, clientIP netip.Addr) (LoginResult, error)
	// Logout ends the Session identified by token (the raw bearer value,
	// not its hash) immediately. Idempotent: a token matching no open
	// Session (already logged out, expired, or never valid) is not an
	// error — the end state Logout promises ("this token no longer opens a
	// session") already holds.
	Logout(ctx context.Context, token string) error
	// Heartbeat reruns Login's presence check against token's Session's
	// Store and reports whether the Session survived (ADR-0014). A no-op
	// that always reports Active for an Admin Session (never
	// Heartbeat-monitored, see ADR-0015) or a token this ticket can't
	// resolve to an open Session (ReasonLoggedOutElsewhere).
	Heartbeat(ctx context.Context, token string, params heartbeatParams, clientIP netip.Addr) (HeartbeatResult, error)
	// ValidateSession resolves token to the Session it names, for both the
	// admin-gating middleware (issue #25) and any handler that needs to
	// know who's calling. ErrSessionNotFound covers a token naming no
	// currently-open, unexpired Session.
	ValidateSession(ctx context.Context, token string) (ValidatedSession, error)
}

// loginResponse is the JSON shape POST /v1/auth/login returns on success.
// StoreID is a pointer — nil for a future Admin Session (ADR-0015, not yet
// issued by this ticket's Login) — rather than the misleading 0.
type loginResponse struct {
	Token     string             `json:"token"`
	StoreID   *int64             `json:"store_id"`
	ExpiresAt pgtype.Timestamptz `json:"expires_at"`
}

func newLoginResponse(result LoginResult) loginResponse {
	return loginResponse{
		Token:     result.Token,
		StoreID:   pgInt8Ptr(result.Session.StoreID),
		ExpiresAt: result.Session.ExpiresAt,
	}
}

func pgInt8Ptr(i pgtype.Int8) *int64 {
	if !i.Valid {
		return nil
	}
	return &i.Int64
}

// heartbeatResponse is the JSON shape POST /v1/auth/heartbeat returns.
// Reason is omitted entirely when Active is true — there's nothing to
// explain.
type heartbeatResponse struct {
	Active bool   `json:"active"`
	Reason string `json:"reason,omitempty"`
}

func newHeartbeatResponse(result HeartbeatResult) heartbeatResponse {
	return heartbeatResponse(result)
}
