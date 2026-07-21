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

// ErrNoStoreMatch is returned by Login when username/password check out but
// none of the Employee's Stores match on IP or Geofence (ADR-0013) — a
// distinct, non-generic error, unlike ErrInvalidCredentials, since the
// caller has already proven who they are; what's missing is presence, not
// identity.
var ErrNoStoreMatch = errors.New("not at an authorized store")

// loginParams is the body for POST /v1/auth/login. Latitude/Longitude are
// pointers, not plain float64s, so an omitted field ("required" on a
// pointer catches nil) is distinguishable from an explicit 0 — a real point
// on the equator/prime meridian, not a sentinel for "not sent" (same
// convention as stores.patchStoreParams' geofence fields). The
// server-observed request IP is deliberately not a field here — ADR-0013:
// "the app sends no IP field for this check" — Handler.Login supplies it
// separately, read from the connection itself.
type loginParams struct {
	Username  string   `json:"username" validate:"required"`
	Password  string   `json:"password" validate:"required"`
	Latitude  *float64 `json:"latitude" validate:"required,min=-90,max=90"`
	Longitude *float64 `json:"longitude" validate:"required,min=-180,max=180"`
}

// LoginResult is Login's success return: the raw bearer token (which, like
// a password-reset token, never touches the database — only its SHA-256
// digest does, see hashToken) alongside the persisted Session row it now
// names.
type LoginResult struct {
	Token   string
	Session repo.Session
}

type Service interface {
	Login(ctx context.Context, params loginParams, clientIP netip.Addr) (LoginResult, error)
	// Logout ends the Session identified by token (the raw bearer value,
	// not its hash) immediately. Idempotent: a token matching no open
	// Session (already logged out, expired, or never valid) is not an
	// error — the end state Logout promises ("this token no longer opens a
	// session") already holds.
	Logout(ctx context.Context, token string) error
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
