package positions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/go-chi/chi/v5"
	"go.uber.org/mock/gomock"
)

func withURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestPositionHandler_CreatePosition(t *testing.T) {
	validParams := createPositionParams{Name: "Technician"}

	tests := []struct {
		name          string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "Create position successfully",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreatePosition(gomock.Any(), validParams).
					Return(repo.Position{ID: 1, Name: "Technician"}, nil)
			},
			expectedCode: http.StatusCreated,
		},
		{
			name:        "Missing name returns 400",
			bodyPayload: createPositionParams{Name: ""},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name:        "Duplicate name maps to 409",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreatePosition(gomock.Any(), validParams).
					Return(repo.Position{}, ErrPositionNameAlreadyExists)
			},
			expectedCode: http.StatusConflict,
		},
		{
			name:        "Database error maps to 500",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreatePosition(gomock.Any(), validParams).
					Return(repo.Position{}, errors.New("connection refused"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tc.setupMock(mockSvc)

			h := NewHandler(mockSvc)
			jsonBody, err := json.Marshal(tc.bodyPayload)
			if err != nil {
				t.Fatalf("failed to marshal request body: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/positions", bytes.NewReader(jsonBody))
			rec := httptest.NewRecorder()

			h.CreatePosition(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}
			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestPositionHandler_ListPositions(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name: "List positions successfully",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					ListPositions(gomock.Any()).
					Return([]repo.Position{{ID: 1, Name: "Manager"}, {ID: 2, Name: "Technician"}}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got []positionResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if len(got) != 2 {
					t.Errorf("expected 2 positions, got %d", len(got))
				}
			},
		},
		{
			name: "Database error maps to 500",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().ListPositions(gomock.Any()).Return(nil, errors.New("connection refused"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tc.setupMock(mockSvc)

			h := NewHandler(mockSvc)
			req := httptest.NewRequest(http.MethodGet, "/positions", nil)
			rec := httptest.NewRecorder()

			h.ListPositions(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}
			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestPositionHandler_UpdatePosition(t *testing.T) {
	validParams := updatePositionParams{Name: "Senior Technician"}

	tests := []struct {
		name         string
		idParam      string
		bodyPayload  any
		setupMock    func(mockSvc *MockService)
		expectedCode int
	}{
		{
			name:        "Rename position successfully",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdatePosition(gomock.Any(), int64(1), validParams).
					Return(repo.Position{ID: 1, Name: "Senior Technician"}, nil)
			},
			expectedCode: http.StatusOK,
		},
		{
			name:         "Non-numeric id path param returns 400",
			idParam:      "not-a-number",
			bodyPayload:  validParams,
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "Unknown id maps ErrPositionNotFound to 404",
			idParam:     "999",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdatePosition(gomock.Any(), int64(999), validParams).
					Return(repo.Position{}, ErrPositionNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:        "Duplicate name maps to 409",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdatePosition(gomock.Any(), int64(1), validParams).
					Return(repo.Position{}, ErrPositionNameAlreadyExists)
			},
			expectedCode: http.StatusConflict,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tc.setupMock(mockSvc)

			h := NewHandler(mockSvc)
			jsonBody, err := json.Marshal(tc.bodyPayload)
			if err != nil {
				t.Fatalf("failed to marshal request body: %v", err)
			}
			req := httptest.NewRequest(http.MethodPut, "/positions/"+tc.idParam, bytes.NewReader(jsonBody))
			req = withURLParam(req, "id", tc.idParam)
			rec := httptest.NewRecorder()

			h.UpdatePosition(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}
		})
	}
}

func TestPositionHandler_DeletePosition(t *testing.T) {
	tests := []struct {
		name         string
		idParam      string
		setupMock    func(mockSvc *MockService)
		expectedCode int
	}{
		{
			name:    "Delete an existing position",
			idParam: "1",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().DeletePosition(gomock.Any(), int64(1)).Return(nil)
			},
			expectedCode: http.StatusNoContent,
		},
		{
			name:         "Non-numeric id path param returns 400",
			idParam:      "not-a-number",
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "Unknown id maps ErrPositionNotFound to 404",
			idParam: "999",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().DeletePosition(gomock.Any(), int64(999)).Return(ErrPositionNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)
			tc.setupMock(mockSvc)

			h := NewHandler(mockSvc)
			req := httptest.NewRequest(http.MethodDelete, "/positions/"+tc.idParam, nil)
			req = withURLParam(req, "id", tc.idParam)
			rec := httptest.NewRecorder()

			h.DeletePosition(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}
		})
	}
}
