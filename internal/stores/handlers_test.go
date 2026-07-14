package stores

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
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
						ID:           12,
						StoreName:    "Montpellier 1",
						OdooStoreID:  pgtype.Text{String: "M30", Valid: true},
						City:         pgtype.Text{String: "Montpellier", Valid: true},
						Latitude:     pgtype.Numeric{Int: big.NewInt(11), Exp: -1, Valid: true},
						Longitude:    pgtype.Numeric{Int: big.NewInt(1002), Exp: -1, Valid: true},
						RadiusMeters: pgtype.Int4{Int32: 50, Valid: true},
						IsActive:     true,
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
			name:    "a store with no geofence set returns null lat/long/radius, not zero values",
			idParam: "13",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().GetStoreByID(gomock.Any(), int64(13)).Return(StoreDetail{
					Store:        repo.Store{ID: 13, StoreName: "No Geofence Yet", IsActive: true},
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
