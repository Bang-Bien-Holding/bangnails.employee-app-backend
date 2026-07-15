package stores

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"
)

// withURLParam attaches a chi route param to a request the same way the chi
// router would when dispatching through a mounted "/stores/{id}" route —
// needed here because these tests call handler methods directly, bypassing
// the router.
func withURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestStoreHandler_SyncStores(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name: "successful sync returns 201 with the summary in meta",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().SyncStores(gomock.Any()).Return(SyncSummary{
					TotalStoresProcessed: 250,
					InsertedStores:       20,
					UpdatedStores:        220,
					DeletedStores:        10,
				}, nil)
			},
			expectedCode: http.StatusCreated,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got struct {
					Status  string      `json:"status"`
					Message string      `json:"message"`
					Meta    SyncSummary `json:"meta"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.Status != "success" {
					t.Errorf("status = %q, want %q", got.Status, "success")
				}
				want := SyncSummary{TotalStoresProcessed: 250, InsertedStores: 20, UpdatedStores: 220, DeletedStores: 10}
				if got.Meta != want {
					t.Errorf("meta = %+v, want %+v", got.Meta, want)
				}
			},
		},
		{
			name: "a sync already in progress maps to 409",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().SyncStores(gomock.Any()).Return(SyncSummary{}, ErrSyncInProgress)
			},
			expectedCode: http.StatusConflict,
		},
		{
			name: "an unexpected service error maps to 500",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().SyncStores(gomock.Any()).Return(SyncSummary{}, errors.New("db exploded"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tt.setupMock(mockSvc)

			h := NewHandler(mockSvc)

			req := httptest.NewRequest(http.MethodPost, "/v1/stores/syncs", nil)
			rec := httptest.NewRecorder()

			h.SyncStores(rec, req)

			if rec.Code != tt.expectedCode {
				t.Errorf("status code = %d, want %d", rec.Code, tt.expectedCode)
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestStoreHandler_PatchStore(t *testing.T) {
	tests := []struct {
		name          string
		idParam       string
		body          string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:    "a full geofence update returns 200 with the updated detail",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","latitude":1.1,"longitude":100.2,"radius_meters":50}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().UpdateStore(gomock.Any(), int64(12), gomock.Any()).Return(StoreDetail{
					Store:        repo.Store{ID: 12, StoreName: "Montpellier 1", WifiWhitelistEnabled: true},
					IPAddresses:  []string{},
					MACAddresses: []string{},
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got storeResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.ID != 12 {
					t.Errorf("id = %d, want 12", got.ID)
				}
			},
		},
		{
			name:    "a body with no geofence fields is valid and returns 200",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z"}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().UpdateStore(gomock.Any(), int64(12), gomock.Any()).Return(StoreDetail{
					Store:        repo.Store{ID: 12, WifiWhitelistEnabled: true},
					IPAddresses:  []string{},
					MACAddresses: []string{},
				}, nil)
			},
			expectedCode: http.StatusOK,
		},
		{
			name:    "exactly one of three geofence fields returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","latitude":1.1}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "two of three geofence fields returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","latitude":1.1,"longitude":100.2}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "radius_meters out of range returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","latitude":1.1,"longitude":100.2,"radius_meters":5000}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "radius_meters of 0 returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","latitude":1.1,"longitude":100.2,"radius_meters":0}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "latitude out of range returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","latitude":91,"longitude":100.2,"radius_meters":50}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "missing updated_at returns 400, service not called",
			idParam: "12",
			body:    `{"latitude":1.1,"longitude":100.2,"radius_meters":50}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "non-numeric id path param returns 400, service not called",
			idParam: "not-a-number",
			body:    `{"updated_at":"2026-07-14T10:00:00Z"}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "unknown store id maps ErrStoreNotFound to 404",
			idParam: "999",
			body:    `{"updated_at":"2026-07-14T10:00:00Z"}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().UpdateStore(gomock.Any(), int64(999), gomock.Any()).Return(StoreDetail{}, ErrStoreNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:    "a stale updated_at maps ErrStoreConflict to 409",
			idParam: "12",
			body:    `{"updated_at":"2020-01-01T00:00:00Z"}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().UpdateStore(gomock.Any(), int64(12), gomock.Any()).Return(StoreDetail{}, ErrStoreConflict)
			},
			expectedCode: http.StatusConflict,
		},
		{
			name:    "an unexpected service error maps to 500",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z"}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().UpdateStore(gomock.Any(), int64(12), gomock.Any()).Return(StoreDetail{}, errors.New("db exploded"))
			},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:    "a malformed ip_addresses entry returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":["not-an-ip"]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "a malformed mac_addresses entry returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","mac_addresses":["not-a-mac"]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "a duplicate value within ip_addresses returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":["138.101.10.1","138.101.10.1"]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "a duplicate value within mac_addresses returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","mac_addresses":["aa:bb:cc:dd:ee:ff","aa:bb:cc:dd:ee:ff"]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "an empty ip_addresses array is valid and returns 200",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":[]}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().UpdateStore(gomock.Any(), int64(12), gomock.Any()).Return(StoreDetail{
					Store:        repo.Store{ID: 12, WifiWhitelistEnabled: true},
					IPAddresses:  []string{},
					MACAddresses: []string{},
				}, nil)
			},
			expectedCode: http.StatusOK,
		},
		{
			name:    "submitting both ip_addresses and mac_addresses together returns 200 with both lists",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":["138.101.10.1","138.101.10.2"],"mac_addresses":["aa:bb:cc:dd:ee:ff"]}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().UpdateStore(gomock.Any(), int64(12), gomock.Any()).Return(StoreDetail{
					Store:        repo.Store{ID: 12, StoreName: "Montpellier 1", WifiWhitelistEnabled: true},
					IPAddresses:  []string{"138.101.10.1", "138.101.10.2"},
					MACAddresses: []string{"aa:bb:cc:dd:ee:ff"},
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got storeResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				wantIPs := []string{"138.101.10.1", "138.101.10.2"}
				if !equalStrings(got.IPAddresses, wantIPs) {
					t.Errorf("ip_addresses = %v, want %v", got.IPAddresses, wantIPs)
				}
				wantMACs := []string{"aa:bb:cc:dd:ee:ff"}
				if !equalStrings(got.MACAddresses, wantMACs) {
					t.Errorf("mac_addresses = %v, want %v", got.MACAddresses, wantMACs)
				}
			},
		},
		{
			// wifi_whitelist_enabled is not a writable field on this endpoint
			// (see ADR-0006) — patchStoreParams has no such field, and
			// json.Read's DisallowUnknownFields rejects it outright rather
			// than silently ignoring it.
			name:    "wifi_whitelist_enabled in the request body returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","wifi_whitelist_enabled":false}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "the response reports wifi_whitelist_enabled read-only from the service's returned state",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z"}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().UpdateStore(gomock.Any(), int64(12), gomock.Any()).Return(StoreDetail{
					Store:        repo.Store{ID: 12, StoreName: "Montpellier 1", WifiWhitelistEnabled: true},
					IPAddresses:  []string{},
					MACAddresses: []string{},
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got storeResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if !got.WifiWhitelistEnabled {
					t.Errorf("wifi_whitelist_enabled = false, want true")
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

			req := httptest.NewRequest(http.MethodPatch, "/v1/stores/"+tt.idParam, strings.NewReader(tt.body))
			req = withURLParam(req, "id", tt.idParam)
			rec := httptest.NewRecorder()

			h.PatchStore(rec, req)

			if rec.Code != tt.expectedCode {
				t.Errorf("status code = %d, want %d, body = %s", rec.Code, tt.expectedCode, rec.Body.String())
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestStoreHandler_DeleteWifiWhitelistEntries(t *testing.T) {
	tests := []struct {
		name          string
		idParam       string
		body          string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:    "a mix of successes and failures returns 200 with the per-entry result array",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":["138.101.10.1"],"mac_addresses":["AA:BB:CC:DD:EE:FF"]}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().DeleteWifiWhitelistEntries(gomock.Any(), int64(12), gomock.Any()).Return([]WifiWhitelistDeleteResult{
					{Value: "138.101.10.1", Type: "ip", Success: true},
					{Value: "AA:BB:CC:DD:EE:FF", Type: "mac", Success: false, Error: "not found in whitelist"},
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got []WifiWhitelistDeleteResult
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				want := []WifiWhitelistDeleteResult{
					{Value: "138.101.10.1", Type: "ip", Success: true},
					{Value: "AA:BB:CC:DD:EE:FF", Type: "mac", Success: false, Error: "not found in whitelist"},
				}
				if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
					t.Errorf("response = %+v, want %+v", got, want)
				}
			},
		},
		{
			name:    "an empty request (no ip_addresses or mac_addresses) returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z"}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "empty arrays for both ip_addresses and mac_addresses return 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":[],"mac_addresses":[]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "missing updated_at returns 400, service not called",
			idParam: "12",
			body:    `{"ip_addresses":["138.101.10.1"]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "a malformed ip_addresses entry returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":["not-an-ip"]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "a malformed mac_addresses entry returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","mac_addresses":["not-a-mac"]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "a duplicate value within ip_addresses returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":["138.101.10.1","138.101.10.1"]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "a duplicate value within mac_addresses returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","mac_addresses":["aa:bb:cc:dd:ee:ff","aa:bb:cc:dd:ee:ff"]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "non-numeric id path param returns 400, service not called",
			idParam: "not-a-number",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":["138.101.10.1"]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "unknown store id maps ErrStoreNotFound to 404",
			idParam: "999",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":["138.101.10.1"]}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().DeleteWifiWhitelistEntries(gomock.Any(), int64(999), gomock.Any()).Return(nil, ErrStoreNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:    "a stale updated_at maps ErrStoreConflict to 409",
			idParam: "12",
			body:    `{"updated_at":"2020-01-01T00:00:00Z","ip_addresses":["138.101.10.1"]}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().DeleteWifiWhitelistEntries(gomock.Any(), int64(12), gomock.Any()).Return(nil, ErrStoreConflict)
			},
			expectedCode: http.StatusConflict,
		},
		{
			name:    "an unexpected service error maps to 500",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","ip_addresses":["138.101.10.1"]}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().DeleteWifiWhitelistEntries(gomock.Any(), int64(12), gomock.Any()).Return(nil, errors.New("db exploded"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tt.setupMock(mockSvc)

			h := NewHandler(mockSvc)

			req := httptest.NewRequest(http.MethodDelete, "/v1/stores/"+tt.idParam+"/wifi-whitelist", strings.NewReader(tt.body))
			req = withURLParam(req, "id", tt.idParam)
			rec := httptest.NewRecorder()

			h.DeleteWifiWhitelistEntries(rec, req)

			if rec.Code != tt.expectedCode {
				t.Errorf("status code = %d, want %d, body = %s", rec.Code, tt.expectedCode, rec.Body.String())
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestStoreHandler_ListStores(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name: "returns 200 with an array covering active and inactive stores, and stores with empty wifi lists",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().ListStores(gomock.Any()).Return([]StoreDetail{
					{
						Store:        repo.Store{ID: 10, StoreName: "Hanoi 1", City: pgtype.Text{String: "Hanoi", Valid: true}, WifiWhitelistEnabled: true},
						IPAddresses:  []string{"138.101.10.1"},
						MACAddresses: []string{},
					},
					{
						Store:        repo.Store{ID: 20, StoreName: "Montpellier 1", City: pgtype.Text{String: "Montpellier", Valid: true}, WifiWhitelistEnabled: false},
						IPAddresses:  []string{},
						MACAddresses: []string{"aa:bb:cc:dd:ee:ff"},
					},
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got []storeResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if len(got) != 2 {
					t.Fatalf("len(response) = %d, want 2", len(got))
				}
				if got[0].ID != 10 || got[0].WifiWhitelistEnabled != true {
					t.Errorf("got[0] = %+v, want active store id 10", got[0])
				}
				if got[1].ID != 20 || got[1].WifiWhitelistEnabled != false {
					t.Errorf("got[1] = %+v, want inactive store id 20", got[1])
				}
				if got[1].IPAddresses == nil || len(got[1].IPAddresses) != 0 {
					t.Errorf("got[1].IPAddresses = %#v, want non-nil empty slice", got[1].IPAddresses)
				}
			},
		},
		{
			name: "an empty store list returns 200 with an empty array, not null",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().ListStores(gomock.Any()).Return([]StoreDetail{}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if strings.TrimSpace(rec.Body.String()) != "[]" {
					t.Errorf("body = %q, want %q", rec.Body.String(), "[]")
				}
			},
		},
		{
			name: "an unexpected service error maps to 500",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().ListStores(gomock.Any()).Return(nil, errors.New("db exploded"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tt.setupMock(mockSvc)

			h := NewHandler(mockSvc)

			req := httptest.NewRequest(http.MethodGet, "/v1/stores", nil)
			rec := httptest.NewRecorder()

			h.ListStores(rec, req)

			if rec.Code != tt.expectedCode {
				t.Errorf("status code = %d, want %d, body = %s", rec.Code, tt.expectedCode, rec.Body.String())
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestStoreHandler_GetStoreByID(t *testing.T) {
	tests := []struct {
		name          string
		idParam       string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:    "found store returns 200 with its detail and wifi whitelist",
			idParam: "12",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().GetStoreByID(gomock.Any(), int64(12)).Return(StoreDetail{
					Store: repo.Store{
						ID:                   12,
						StoreName:            "Montpellier 1",
						OdooStoreID:          pgtype.Text{String: "M30", Valid: true},
						City:                 pgtype.Text{String: "Montpellier", Valid: true},
						Latitude:             pgtype.Numeric{Int: big.NewInt(11), Exp: -1, Valid: true},
						Longitude:            pgtype.Numeric{Int: big.NewInt(1002), Exp: -1, Valid: true},
						RadiusMeters:         pgtype.Int4{Int32: 50, Valid: true},
						WifiWhitelistEnabled: true,
					},
					IPAddresses:  []string{"138.101.10.1", "138.101.10.2"},
					MACAddresses: []string{"aa:bb:cc:dd:ee:ff"},
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got storeResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.ID != 12 || got.StoreName != "Montpellier 1" {
					t.Errorf("id/store_name = %d/%q, want 12/%q", got.ID, got.StoreName, "Montpellier 1")
				}
				if got.OdooStoreID == nil || *got.OdooStoreID != "M30" {
					t.Errorf("odoo_store_id = %v, want \"M30\"", got.OdooStoreID)
				}
				if got.Latitude == nil || *got.Latitude != 1.1 {
					t.Errorf("latitude = %v, want 1.1", got.Latitude)
				}
				if got.Longitude == nil || *got.Longitude != 100.2 {
					t.Errorf("longitude = %v, want 100.2", got.Longitude)
				}
				if got.RadiusMeters == nil || *got.RadiusMeters != 50 {
					t.Errorf("radius_meters = %v, want 50", got.RadiusMeters)
				}
				wantIPs := []string{"138.101.10.1", "138.101.10.2"}
				if !equalStrings(got.IPAddresses, wantIPs) {
					t.Errorf("ip_addresses = %v, want %v", got.IPAddresses, wantIPs)
				}
				wantMACs := []string{"aa:bb:cc:dd:ee:ff"}
				if !equalStrings(got.MACAddresses, wantMACs) {
					t.Errorf("mac_addresses = %v, want %v", got.MACAddresses, wantMACs)
				}
			},
		},
		{
			name:    "a wifi-disabled store returns 200 with wifi_whitelist_enabled: false, not 404",
			idParam: "14",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().GetStoreByID(gomock.Any(), int64(14)).Return(StoreDetail{
					Store:        repo.Store{ID: 14, StoreName: "Deactivated Store", WifiWhitelistEnabled: false},
					IPAddresses:  []string{},
					MACAddresses: []string{},
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got storeResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.WifiWhitelistEnabled {
					t.Errorf("is_active = true, want false")
				}
			},
		},
		{
			name:    "a store with no geofence set returns null lat/long/radius, not zero values",
			idParam: "13",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().GetStoreByID(gomock.Any(), int64(13)).Return(StoreDetail{
					Store:        repo.Store{ID: 13, StoreName: "No Geofence Yet", WifiWhitelistEnabled: true},
					IPAddresses:  []string{},
					MACAddresses: []string{},
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got storeResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.Latitude != nil || got.Longitude != nil || got.RadiusMeters != nil {
					t.Errorf("latitude/longitude/radius_meters = %v/%v/%v, want all nil", got.Latitude, got.Longitude, got.RadiusMeters)
				}
				if got.IPAddresses == nil || got.MACAddresses == nil {
					t.Errorf("ip_addresses/mac_addresses = %#v/%#v, want non-nil empty slices", got.IPAddresses, got.MACAddresses)
				}
			},
		},
		{
			name:    "non-numeric id path param returns 400",
			idParam: "not-a-number",
			setupMock: func(mockSvc *MockService) {
				// Service must not be called — parsing fails at the handler layer.
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "unknown store id maps ErrStoreNotFound to 404",
			idParam: "999",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().GetStoreByID(gomock.Any(), int64(999)).Return(StoreDetail{}, ErrStoreNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:    "an unexpected service error maps to 500",
			idParam: "12",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().GetStoreByID(gomock.Any(), int64(12)).Return(StoreDetail{}, errors.New("db exploded"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tt.setupMock(mockSvc)

			h := NewHandler(mockSvc)

			req := httptest.NewRequest(http.MethodGet, "/v1/stores/"+tt.idParam, nil)
			req = withURLParam(req, "id", tt.idParam)
			rec := httptest.NewRecorder()

			h.GetStoreByID(rec, req)

			if rec.Code != tt.expectedCode {
				t.Errorf("status code = %d, want %d", rec.Code, tt.expectedCode)
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestStoreHandler_SetStoreWifiWhitelistEnabled(t *testing.T) {
	tests := []struct {
		name          string
		idParam       string
		body          string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:    "a successful toggle returns 200 with fresh state",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","wifi_whitelist_enabled":true}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().SetStoreWifiWhitelistEnabled(gomock.Any(), int64(12), gomock.Any()).Return(StoreWifiToggleResult{
					ID: 12, WifiWhitelistEnabled: true,
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got storeToggleResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.ID != 12 || !got.WifiWhitelistEnabled {
					t.Errorf("got = %+v, want id 12, wifi_whitelist_enabled true", got)
				}
			},
		},
		{
			name:    "missing updated_at returns 400, service not called",
			idParam: "12",
			body:    `{"wifi_whitelist_enabled":true}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "missing wifi_whitelist_enabled returns 400, service not called",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z"}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "non-numeric id path param returns 400, service not called",
			idParam: "not-a-number",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","wifi_whitelist_enabled":true}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "unknown store id maps ErrStoreNotFound to 404",
			idParam: "999",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","wifi_whitelist_enabled":true}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().SetStoreWifiWhitelistEnabled(gomock.Any(), int64(999), gomock.Any()).Return(StoreWifiToggleResult{}, ErrStoreNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:    "a stale updated_at maps ErrStoreConflict to 409",
			idParam: "12",
			body:    `{"updated_at":"2020-01-01T00:00:00Z","wifi_whitelist_enabled":true}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().SetStoreWifiWhitelistEnabled(gomock.Any(), int64(12), gomock.Any()).Return(StoreWifiToggleResult{}, ErrStoreConflict)
			},
			expectedCode: http.StatusConflict,
		},
		{
			name:    "an unexpected service error maps to 500",
			idParam: "12",
			body:    `{"updated_at":"2026-07-14T10:00:00Z","wifi_whitelist_enabled":true}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().SetStoreWifiWhitelistEnabled(gomock.Any(), int64(12), gomock.Any()).Return(StoreWifiToggleResult{}, errors.New("db exploded"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tt.setupMock(mockSvc)

			h := NewHandler(mockSvc)

			req := httptest.NewRequest(http.MethodPatch, "/v1/stores/"+tt.idParam+"/wifi-whitelist-enabled", strings.NewReader(tt.body))
			req = withURLParam(req, "id", tt.idParam)
			rec := httptest.NewRecorder()

			h.SetStoreWifiWhitelistEnabled(rec, req)

			if rec.Code != tt.expectedCode {
				t.Errorf("status code = %d, want %d, body = %s", rec.Code, tt.expectedCode, rec.Body.String())
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}

func TestStoreHandler_BulkSetWifiWhitelistEnabled(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name: "a successful bulk toggle returns 200 with fresh state per store",
			body: `{"stores":[{"id":1,"updated_at":"2026-07-14T10:00:00Z"},{"id":2,"updated_at":"2026-07-14T10:05:00Z"}],"wifi_whitelist_enabled":false}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().BulkSetWifiWhitelistEnabled(gomock.Any(), gomock.Any()).Return([]StoreWifiToggleResult{
					{ID: 1, WifiWhitelistEnabled: false},
					{ID: 2, WifiWhitelistEnabled: false},
				}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got []storeToggleResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
					t.Errorf("got = %+v, want ids 1 and 2", got)
				}
			},
		},
		{
			name: "empty stores array returns 400, service not called",
			body: `{"stores":[],"wifi_whitelist_enabled":false}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "missing stores returns 400, service not called",
			body: `{"wifi_whitelist_enabled":false}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "missing wifi_whitelist_enabled returns 400, service not called",
			body: `{"stores":[{"id":1,"updated_at":"2026-07-14T10:00:00Z"}]}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "a store entry missing updated_at returns 400, service not called",
			body: `{"stores":[{"id":1}],"wifi_whitelist_enabled":false}`,
			setupMock: func(mockSvc *MockService) {
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "any unknown id or stale updated_at in the batch returns 409 with failed_ids",
			body: `{"stores":[{"id":1,"updated_at":"2026-07-14T10:00:00Z"},{"id":2,"updated_at":"2026-07-14T10:05:00Z"}],"wifi_whitelist_enabled":false}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().BulkSetWifiWhitelistEnabled(gomock.Any(), gomock.Any()).Return(
					nil, &BulkWifiWhitelistConflictError{FailedIDs: []int64{2}},
				)
			},
			expectedCode: http.StatusConflict,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got bulkWifiWhitelistConflictResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				want := []int64{2}
				if len(got.FailedIDs) != 1 || got.FailedIDs[0] != want[0] {
					t.Errorf("failed_ids = %v, want %v", got.FailedIDs, want)
				}
			},
		},
		{
			name: "an unexpected service error maps to 500",
			body: `{"stores":[{"id":1,"updated_at":"2026-07-14T10:00:00Z"}],"wifi_whitelist_enabled":false}`,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().BulkSetWifiWhitelistEnabled(gomock.Any(), gomock.Any()).Return(nil, errors.New("db exploded"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tt.setupMock(mockSvc)

			h := NewHandler(mockSvc)

			req := httptest.NewRequest(http.MethodPatch, "/v1/stores", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			h.BulkSetWifiWhitelistEnabled(rec, req)

			if rec.Code != tt.expectedCode {
				t.Errorf("status code = %d, want %d, body = %s", rec.Code, tt.expectedCode, rec.Body.String())
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, rec)
			}
		})
	}
}
