package auth

import (
	"errors"
	"net/netip"
	"strconv"
	"testing"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	sqlcmocks "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc/mocks"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/tokenx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/bcrypt"
)

func newTestService(q repo.Querier) *service {
	return &service{repo: q}
}

// mustHashPassword returns the bcrypt hash of password, for building
// repo.Employee fixtures whose password check must succeed.
func mustHashPassword(t *testing.T, password string) []byte {
	t.Helper()
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return hashed
}

// mustNumeric converts f to a pgtype.Numeric the same way
// stores.float64PtrToNumeric does, for building repo.Store geofence
// fixtures.
func mustNumeric(t *testing.T, f float64) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(strconv.FormatFloat(f, 'f', -1, 64)); err != nil {
		t.Fatalf("scan numeric: %v", err)
	}
	return n
}

func TestAuthService_Login(t *testing.T) {
	const (
		username = "jdoe"
		password = "correct-horse-battery-staple"
	)
	hashedPassword := mustHashPassword(t, password)
	clientIP := netip.MustParseAddr("203.0.113.5")
	deviceLat, deviceLong := 48.8566, 2.3522 // Paris

	baseEmployee := repo.Employee{
		ID:       7,
		Username: username,
		Password: hashedPassword,
		IsActive: true,
	}

	ipMatchStore := repo.ListStoresForLoginByEmployeeIDRow{
		Store: repo.Store{
			ID:                   1,
			WifiWhitelistEnabled: true,
		},
		IpAddresses: []netip.Addr{clientIP},
	}

	geofenceMatchStore := repo.ListStoresForLoginByEmployeeIDRow{
		Store: repo.Store{
			ID:           2,
			Latitude:     mustNumeric(t, deviceLat),
			Longitude:    mustNumeric(t, deviceLong),
			RadiusMeters: pgtype.Int4{Int32: 100, Valid: true},
		},
	}

	noMatchStore := repo.ListStoresForLoginByEmployeeIDRow{
		Store: repo.Store{
			ID:                   3,
			WifiWhitelistEnabled: true,
		},
		IpAddresses: []netip.Addr{netip.MustParseAddr("198.51.100.9")},
	}

	tests := []struct {
		name       string
		params     loginParams
		setupMock  func(mockRepo *sqlcmocks.MockQuerier)
		wantErr    error
		wantStore  int64
		wantResult bool
	}{
		{
			name:   "unknown username returns generic invalid credentials",
			params: newLoginParams(t, "ghost", password, deviceLat, deviceLong),
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				mockRepo.EXPECT().GetEmployeeByUsername(gomock.Any(), "ghost").Return(repo.Employee{}, pgx.ErrNoRows)
			},
			wantErr: ErrInvalidCredentials,
		},
		{
			name:   "wrong password records a failed attempt and returns generic invalid credentials",
			params: newLoginParams(t, username, "wrong-password", deviceLat, deviceLong),
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				mockRepo.EXPECT().GetEmployeeByUsername(gomock.Any(), username).Return(baseEmployee, nil)
				mockRepo.EXPECT().RecordFailedLoginAttempt(gomock.Any(), gomock.Any()).
					Return(repo.Employee{}, nil)
			},
			wantErr: ErrInvalidCredentials,
		},
		{
			name:   "locked-out employee returns generic invalid credentials and records no additional attempt",
			params: newLoginParams(t, username, password, deviceLat, deviceLong),
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				locked := baseEmployee
				locked.LockedUntil = pgtype.Timestamptz{Time: time.Now().Add(5 * time.Minute), Valid: true}
				mockRepo.EXPECT().GetEmployeeByUsername(gomock.Any(), username).Return(locked, nil)
				// No RecordFailedLoginAttempt/ResetFailedLoginAttempts/etc.
				// expected — Login still runs the bcrypt compare against the
				// real password hash (see dummyPasswordHash's timing-safety
				// comment), it just doesn't act on the result once the lock
				// check has already decided the outcome.
			},
			wantErr: ErrInvalidCredentials,
		},
		{
			name:   "an expired lockout no longer blocks login",
			params: newLoginParams(t, username, password, deviceLat, deviceLong),
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				expiredLock := baseEmployee
				expiredLock.LockedUntil = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
				mockRepo.EXPECT().GetEmployeeByUsername(gomock.Any(), username).Return(expiredLock, nil)
				mockRepo.EXPECT().ResetFailedLoginAttempts(gomock.Any(), int64(7)).Return(nil)
				mockRepo.EXPECT().ListStoresForLoginByEmployeeID(gomock.Any(), int64(7)).
					Return([]repo.ListStoresForLoginByEmployeeIDRow{ipMatchStore}, nil)
				mockRepo.EXPECT().UpsertSession(gomock.Any(), gomock.Any()).
					Return(repo.Session{EmployeeID: 7, StoreID: pgtype.Int8{Int64: 1, Valid: true}}, nil)
			},
			wantResult: true,
			wantStore:  1,
		},
		{
			name:   "deactivated employee returns generic invalid credentials and records no additional attempt",
			params: newLoginParams(t, username, password, deviceLat, deviceLong),
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				inactive := baseEmployee
				inactive.IsActive = false
				mockRepo.EXPECT().GetEmployeeByUsername(gomock.Any(), username).Return(inactive, nil)
			},
			wantErr: ErrInvalidCredentials,
		},
		{
			name:   "correct credentials matched via IP issue a session and reset the lockout counter",
			params: newLoginParams(t, username, password, deviceLat, deviceLong),
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				mockRepo.EXPECT().GetEmployeeByUsername(gomock.Any(), username).Return(baseEmployee, nil)
				mockRepo.EXPECT().ResetFailedLoginAttempts(gomock.Any(), int64(7)).Return(nil)
				mockRepo.EXPECT().ListStoresForLoginByEmployeeID(gomock.Any(), int64(7)).
					Return([]repo.ListStoresForLoginByEmployeeIDRow{ipMatchStore}, nil)
				mockRepo.EXPECT().UpsertSession(gomock.Any(), gomock.Cond(func(p repo.UpsertSessionParams) bool {
					return p.EmployeeID == 7 && p.StoreID == pgtype.Int8{Int64: 1, Valid: true}
				})).Return(repo.Session{EmployeeID: 7, StoreID: pgtype.Int8{Int64: 1, Valid: true}}, nil)
			},
			wantResult: true,
			wantStore:  1,
		},
		{
			name:   "correct credentials with no IP match fall back to geofence",
			params: newLoginParams(t, username, password, deviceLat, deviceLong),
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				mockRepo.EXPECT().GetEmployeeByUsername(gomock.Any(), username).Return(baseEmployee, nil)
				mockRepo.EXPECT().ResetFailedLoginAttempts(gomock.Any(), int64(7)).Return(nil)
				mockRepo.EXPECT().ListStoresForLoginByEmployeeID(gomock.Any(), int64(7)).
					Return([]repo.ListStoresForLoginByEmployeeIDRow{noMatchStore, geofenceMatchStore}, nil)
				mockRepo.EXPECT().UpsertSession(gomock.Any(), gomock.Cond(func(p repo.UpsertSessionParams) bool {
					return p.StoreID == pgtype.Int8{Int64: 2, Valid: true}
				})).Return(repo.Session{EmployeeID: 7, StoreID: pgtype.Int8{Int64: 2, Valid: true}}, nil)
			},
			wantResult: true,
			wantStore:  2,
		},
		{
			name:   "correct credentials with no store match at all still reset the lockout counter",
			params: newLoginParams(t, username, password, deviceLat, deviceLong),
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				mockRepo.EXPECT().GetEmployeeByUsername(gomock.Any(), username).Return(baseEmployee, nil)
				mockRepo.EXPECT().ResetFailedLoginAttempts(gomock.Any(), int64(7)).Return(nil)
				mockRepo.EXPECT().ListStoresForLoginByEmployeeID(gomock.Any(), int64(7)).
					Return([]repo.ListStoresForLoginByEmployeeIDRow{noMatchStore}, nil)
			},
			wantErr: ErrNoStoreMatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := sqlcmocks.NewMockQuerier(ctrl)
			tt.setupMock(mockRepo)

			svc := newTestService(mockRepo)

			result, err := svc.Login(t.Context(), tt.params, clientIP)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.wantResult {
				return
			}
			if result.Token == "" {
				t.Error("expected a non-empty session token")
			}
			if result.Session.StoreID.Int64 != tt.wantStore {
				t.Errorf("session store id = %d, want %d", result.Session.StoreID.Int64, tt.wantStore)
			}
		})
	}
}

// newLoginParams builds a loginParams fixture — Latitude/Longitude are
// pointers (see loginParams), so tests go through this helper rather than
// building the struct literal by hand everywhere.
func newLoginParams(t *testing.T, username, password string, lat, long float64) loginParams {
	t.Helper()
	return loginParams{Username: username, Password: password, Latitude: &lat, Longitude: &long}
}

func TestAuthService_Logout(t *testing.T) {
	tests := []struct {
		name      string
		setupMock func(mockRepo *sqlcmocks.MockQuerier)
		wantErr   bool
	}{
		{
			name: "deletes the matching session",
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				mockRepo.EXPECT().DeleteSessionByTokenHash(gomock.Any(), tokenx.Hash("a-valid-token")).Return(int64(1), nil)
			},
		},
		{
			name: "a token matching no session is not an error — logout is idempotent",
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				mockRepo.EXPECT().DeleteSessionByTokenHash(gomock.Any(), tokenx.Hash("a-valid-token")).Return(int64(0), nil)
			},
		},
		{
			name: "a repo error propagates",
			setupMock: func(mockRepo *sqlcmocks.MockQuerier) {
				mockRepo.EXPECT().DeleteSessionByTokenHash(gomock.Any(), tokenx.Hash("a-valid-token")).
					Return(int64(0), errors.New("db exploded"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := sqlcmocks.NewMockQuerier(ctrl)
			tt.setupMock(mockRepo)

			svc := newTestService(mockRepo)

			err := svc.Logout(t.Context(), "a-valid-token")
			if tt.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMatchStore(t *testing.T) {
	clientIP := netip.MustParseAddr("203.0.113.5")
	otherIP := netip.MustParseAddr("198.51.100.9")
	deviceLat, deviceLong := 48.8566, 2.3522 // Paris

	storeWithinRadius := repo.Store{
		ID:           1,
		Latitude:     mustNumeric(t, deviceLat),
		Longitude:    mustNumeric(t, deviceLong),
		RadiusMeters: pgtype.Int4{Int32: 100, Valid: true},
	}
	storeOutsideRadius := repo.Store{
		ID: 2,
		// Roughly 550km from Paris — well outside any plausible radius.
		Latitude:     mustNumeric(t, 45.7640),
		Longitude:    mustNumeric(t, 4.8357),
		RadiusMeters: pgtype.Int4{Int32: 100, Valid: true},
	}

	tests := []struct {
		name       string
		candidates []repo.ListStoresForLoginByEmployeeIDRow
		wantMatch  bool
		wantStore  int64
	}{
		{
			name: "matches on IP when wifi whitelist is enabled",
			candidates: []repo.ListStoresForLoginByEmployeeIDRow{
				{Store: repo.Store{ID: 1, WifiWhitelistEnabled: true}, IpAddresses: []netip.Addr{clientIP}},
			},
			wantMatch: true,
			wantStore: 1,
		},
		{
			name: "does not match on IP when wifi whitelist is disabled for that store",
			candidates: []repo.ListStoresForLoginByEmployeeIDRow{
				{Store: repo.Store{ID: 1, WifiWhitelistEnabled: false}, IpAddresses: []netip.Addr{clientIP}},
			},
			wantMatch: false,
		},
		{
			name: "falls back to geofence when IP doesn't match any store",
			candidates: []repo.ListStoresForLoginByEmployeeIDRow{
				{Store: repo.Store{ID: 10, WifiWhitelistEnabled: true}, IpAddresses: []netip.Addr{otherIP}},
				{Store: storeWithinRadius},
			},
			wantMatch: true,
			wantStore: storeWithinRadius.ID,
		},
		{
			name: "geofence match outside the radius does not match",
			candidates: []repo.ListStoresForLoginByEmployeeIDRow{
				{Store: storeOutsideRadius},
			},
			wantMatch: false,
		},
		{
			name: "a store with no geofence set is skipped, not treated as a match",
			candidates: []repo.ListStoresForLoginByEmployeeIDRow{
				{Store: repo.Store{ID: 5}},
			},
			wantMatch: false,
		},
		{
			name: "IP tier wins even when a later store would also match on geofence",
			candidates: []repo.ListStoresForLoginByEmployeeIDRow{
				{Store: repo.Store{ID: 9, WifiWhitelistEnabled: true}, IpAddresses: []netip.Addr{clientIP}},
				{Store: storeWithinRadius},
			},
			wantMatch: true,
			wantStore: 9,
		},
		{
			name:       "no candidate stores means no match",
			candidates: nil,
			wantMatch:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, ok := matchStore(tt.candidates, clientIP, deviceLat, deviceLong)
			if ok != tt.wantMatch {
				t.Fatalf("matched = %v, want %v", ok, tt.wantMatch)
			}
			if ok && store.ID != tt.wantStore {
				t.Errorf("matched store id = %d, want %d", store.ID, tt.wantStore)
			}
		})
	}
}

func TestHaversineMeters(t *testing.T) {
	// Paris (48.8566, 2.3522) to Lyon (45.7640, 4.8357) is ~392km per
	// standard great-circle references — assert within a generous
	// tolerance rather than pinning an exact float.
	got := haversineMeters(48.8566, 2.3522, 45.7640, 4.8357)
	const wantMeters = 392_000.0
	const toleranceMeters = 5_000.0
	if got < wantMeters-toleranceMeters || got > wantMeters+toleranceMeters {
		t.Errorf("haversineMeters = %.0fm, want within %.0fm of %.0fm", got, toleranceMeters, wantMeters)
	}

	if d := haversineMeters(48.8566, 2.3522, 48.8566, 2.3522); d != 0 {
		t.Errorf("distance from a point to itself = %.4f, want 0", d)
	}
}

// TestDummyPasswordHash guards the constant Login compares an unknown
// username's password against (see dummyPasswordHash's comment) — a typo'd
// or corrupted literal would make bcrypt.CompareHashAndPassword return a
// format error instantly instead of paying its usual cost, silently
// reopening the timing side-channel the constant exists to close.
func TestDummyPasswordHash(t *testing.T) {
	if err := bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte("anything")); err == nil {
		t.Fatal("expected the comparison to fail (wrong password), but not error out on a malformed hash")
	} else if errors.Is(err, bcrypt.ErrHashTooShort) {
		t.Fatalf("dummyPasswordHash is not a valid bcrypt hash: %v", err)
	}
}
