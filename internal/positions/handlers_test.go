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
		{
			name:        "Whitespace-only name returns 400 after trim",
			bodyPayload: createPositionParams{Name: "   "},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called: trimming empties the name
				// before validation runs.
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name:        "Leading/trailing whitespace is trimmed before reaching the service",
			bodyPayload: createPositionParams{Name: "  Technician  "},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreatePosition(gomock.Any(), createPositionParams{Name: "Technician"}).
					Return(repo.Position{ID: 1, Name: "Technician"}, nil)
			},
			expectedCode: http.StatusCreated,
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
			name:         "Zero id path param returns 400",
			idParam:      "0",
			bodyPayload:  validParams,
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "Negative id path param returns 400",
			idParam:      "-1",
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
		{
			name:        "Whitespace-only name returns 400 after trim",
			idParam:     "1",
			bodyPayload: updatePositionParams{Name: "   "},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called: trimming empties the name
				// before validation runs.
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "Leading/trailing whitespace is trimmed before reaching the service",
			idParam:     "1",
			bodyPayload: updatePositionParams{Name: "  Senior Technician  "},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdatePosition(gomock.Any(), int64(1), updatePositionParams{Name: "Senior Technician"}).
					Return(repo.Position{ID: 1, Name: "Senior Technician"}, nil)
			},
			expectedCode: http.StatusOK,
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
			name:         "Zero id path param returns 400",
			idParam:      "0",
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "Negative id path param returns 400",
			idParam:      "-1",
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

func TestPositionHandler_GetPositionEmployees(t *testing.T) {
	tests := []struct {
		name          string
		idParam       string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:    "List employees assigned to an existing position",
			idParam: "1",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().GetPositionEmployees(gomock.Any(), int64(1)).Return([]int64{10, 20}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got positionEmployeesResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if len(got.EmployeeIDs) != 2 {
					t.Errorf("expected 2 employee ids, got %v", got.EmployeeIDs)
				}
			},
		},
		{
			name:         "Non-numeric id path param returns 400",
			idParam:      "not-a-number",
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:         "Zero id path param returns 400",
			idParam:      "0",
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "Unknown id maps ErrPositionNotFound to 404",
			idParam: "999",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().GetPositionEmployees(gomock.Any(), int64(999)).Return(nil, ErrPositionNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:    "Database error maps to 500",
			idParam: "1",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().GetPositionEmployees(gomock.Any(), int64(1)).Return(nil, errors.New("connection refused"))
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
			req := httptest.NewRequest(http.MethodGet, "/positions/"+tc.idParam+"/employees", nil)
			req = withURLParam(req, "id", tc.idParam)
			rec := httptest.NewRecorder()

			h.GetPositionEmployees(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}
			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestPositionHandler_SetPositionEmployees(t *testing.T) {
	validParams := setPositionEmployeesParams{EmployeeIDs: []int64{10, 20}}

	tests := []struct {
		name          string
		idParam       string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "Replace the position's employee set successfully",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetPositionEmployees(gomock.Any(), int64(1), validParams).
					Return([]int64{10, 20}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got positionEmployeesResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if len(got.EmployeeIDs) != 2 {
					t.Errorf("expected 2 employee ids, got %v", got.EmployeeIDs)
				}
			},
		},
		{
			name:        "Empty employeeIds clears the assignment",
			idParam:     "1",
			bodyPayload: setPositionEmployeesParams{EmployeeIDs: []int64{}},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetPositionEmployees(gomock.Any(), int64(1), setPositionEmployeesParams{EmployeeIDs: []int64{}}).
					Return([]int64{}, nil)
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
			name:         "Duplicate ids in body fail validation",
			idParam:      "1",
			bodyPayload:  setPositionEmployeesParams{EmployeeIDs: []int64{10, 10}},
			setupMock:    func(mockSvc *MockService) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "Unknown position id maps ErrPositionNotFound to 404",
			idParam:     "999",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetPositionEmployees(gomock.Any(), int64(999), validParams).
					Return(nil, ErrPositionNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:        "Unknown employee id maps ErrUnknownEmployeeID to 400",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetPositionEmployees(gomock.Any(), int64(1), validParams).
					Return(nil, ErrUnknownEmployeeID)
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "Database error maps to 500",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetPositionEmployees(gomock.Any(), int64(1), validParams).
					Return(nil, errors.New("connection refused"))
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
			req := httptest.NewRequest(http.MethodPut, "/positions/"+tc.idParam+"/employees", bytes.NewReader(jsonBody))
			req = withURLParam(req, "id", tc.idParam)
			rec := httptest.NewRecorder()

			h.SetPositionEmployees(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}
			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}
