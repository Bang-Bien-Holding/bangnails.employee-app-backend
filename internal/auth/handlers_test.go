package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"
)

// serveWithClientIP runs req through middleware.ClientIPFromRemoteAddr
// before handler — the same middleware cmd/api.go installs globally — so
// Handler.Login's middleware.GetClientIPAddr call sees a value the same way
// it would through the real router. req.RemoteAddr must already be set by
// the caller.
func serveWithClientIP(handler http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	middleware.ClientIPFromRemoteAddr(handler).ServeHTTP(rec, req)
	return rec
}

func TestAuthHandler_Login(t *testing.T) {
	validBody := `{"username":"jdoe","password":"correct-horse","latitude":48.8566,"longitude":2.3522}`

	tests := []struct {
		name          string
		body          string
		remoteAddr    string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:         "malformed JSON body returns 400",
			body:         `{`,
			remoteAddr:   "203.0.113.5:54321",
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "missing required fields fails validation with 400",
			body:         `{"username":"jdoe"}`,
			remoteAddr:   "203.0.113.5:54321",
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "an unparseable RemoteAddr with no client IP available returns 500",
			body:         validBody,
			remoteAddr:   "not-an-ip-or-host-port",
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:       "invalid credentials maps to 401",
			body:       validBody,
			remoteAddr: "203.0.113.5:54321",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().Login(gomock.Any(), gomock.Any(), gomock.Any()).Return(LoginResult{}, ErrInvalidCredentials)
			},
			expectedCode: http.StatusUnauthorized,
		},
		{
			name:       "no store match maps to 403",
			body:       validBody,
			remoteAddr: "203.0.113.5:54321",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().Login(gomock.Any(), gomock.Any(), gomock.Any()).Return(LoginResult{}, ErrNoStoreMatch)
			},
			expectedCode: http.StatusForbidden,
		},
		{
			name:       "an unexpected service error maps to 500",
			body:       validBody,
			remoteAddr: "203.0.113.5:54321",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().Login(gomock.Any(), gomock.Any(), gomock.Any()).Return(LoginResult{}, errors.New("db exploded"))
			},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:       "success returns the token and matched store",
			body:       validBody,
			remoteAddr: "203.0.113.5:54321",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().Login(gomock.Any(), gomock.Any(), gomock.Any()).Return(LoginResult{
					Token: "raw-token-value",
					Session: repo.Session{
						EmployeeID: 7,
						StoreID:    pgtype.Int8{Int64: 1, Valid: true},
						ExpiresAt:  pgtype.Timestamptz{Valid: true},
					},
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got loginResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.Token != "raw-token-value" {
					t.Errorf("token = %q, want %q", got.Token, "raw-token-value")
				}
				if got.StoreID == nil || *got.StoreID != 1 {
					t.Errorf("store_id = %v, want 1", got.StoreID)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tt.setupMock(mockSvc)

			h := NewHandler(mockSvc)

			req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewBufferString(tt.body))
			req.RemoteAddr = tt.remoteAddr
			rec := serveWithClientIP(h.Login, req)

			if rec.Code != tt.expectedCode {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.expectedCode, rec.Body.String())
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestAuthHandler_Logout(t *testing.T) {
	tests := []struct {
		name         string
		authHeader   string
		setupMock    func(mockSvc *MockService)
		expectedCode int
	}{
		{
			name:         "missing Authorization header returns 401",
			authHeader:   "",
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusUnauthorized,
		},
		{
			name:         "malformed Authorization header returns 401",
			authHeader:   "Basic sometoken",
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusUnauthorized,
		},
		{
			name:         "empty bearer token returns 401",
			authHeader:   "Bearer ",
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusUnauthorized,
		},
		{
			name:       "a service error maps to 500",
			authHeader: "Bearer a-valid-token",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().Logout(gomock.Any(), "a-valid-token").Return(errors.New("db exploded"))
			},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:       "success returns 204",
			authHeader: "Bearer a-valid-token",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().Logout(gomock.Any(), "a-valid-token").Return(nil)
			},
			expectedCode: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tt.setupMock(mockSvc)

			h := NewHandler(mockSvc)

			req := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			h.Logout(rec, req)

			if rec.Code != tt.expectedCode {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.expectedCode, rec.Body.String())
			}
		})
	}
}
