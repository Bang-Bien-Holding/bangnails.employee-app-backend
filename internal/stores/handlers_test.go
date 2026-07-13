package stores

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/mock/gomock"
)

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
