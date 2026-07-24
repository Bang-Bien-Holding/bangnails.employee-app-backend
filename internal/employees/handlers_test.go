package employees

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

// withURLParam attaches a chi route param to a request the same way the chi
// router would when dispatching through a mounted "/employees/{id}" route —
// needed here because these tests call handler methods directly, bypassing
// the router.
func withURLParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func boolPtr(b bool) *bool {
	return &b
}

func TestEmployeeHandler_GetEmployeeByID(t *testing.T) {
	tests := []struct {
		name          string
		idParam       string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:    "TS-HDL-16: Get employee by ID successfully",
			idParam: "1",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(EmployeeDetail{Employee: repo.Employee{ID: 1, OdooEmployeeID: 30, FullName: "Nguyen Van A"}}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got repo.Employee
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.ID != 1 {
					t.Errorf("expected employee ID 1, got %d", got.ID)
				}
			},
		},
		{
			name:    "TS-HDL-17: Non-numeric id path param returns 400",
			idParam: "not-a-number",
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because parsing fails at the handler layer
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "TS-HDL-17b: Zero id path param returns 400",
			idParam: "0",
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "TS-HDL-17c: Negative id path param returns 400",
			idParam: "-1",
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "TS-HDL-18: Unknown id maps ErrEmployeeNotFound to 404",
			idParam: "999",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(999)).
					Return(EmployeeDetail{}, ErrEmployeeNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:    "TS-HDL-19: Map internal server error on database failure",
			idParam: "1",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(EmployeeDetail{}, errors.New("connection refused"))
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

			req := httptest.NewRequest(http.MethodGet, "/employees/"+tc.idParam, nil)
			req = withURLParam(req, "id", tc.idParam)
			rec := httptest.NewRecorder()

			h.GetEmployeeByID(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestEmployeeHandler_UpdateEmployee(t *testing.T) {
	validParams := updateEmployeeParams{
		OdooEmployeeID: 1,
		FullName:       "Nguyen Van A",
		Email:          "van-a@example.com",
		Username:       "nguyenvana",
	}

	tests := []struct {
		name          string
		idParam       string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "TS-HDL-20: Update employee successfully with valid input",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdateEmployee(gomock.Any(), int64(1), validParams).
					Return(EmployeeDetail{Employee: repo.Employee{ID: 1, FullName: validParams.FullName, Email: validParams.Email, Username: validParams.Username}}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got repo.Employee
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.ID != 1 {
					t.Errorf("expected employee ID 1, got %d", got.ID)
				}
			},
		},
		{
			name:        "TS-HDL-21: Non-numeric id path param returns 400",
			idParam:     "not-a-number",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because parsing fails at the handler layer
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-21b: Zero id path param returns 400",
			idParam:     "0",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-21c: Negative id path param returns 400",
			idParam:     "-1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "TS-HDL-22: Update employee failed due to missing required fields",
			idParam: "1",
			bodyPayload: updateEmployeeParams{
				Email: "invalid-email", // missing FullName/Username/Role, invalid email
			},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name:    "TS-HDL-22b: Negative position id in body fails validation",
			idParam: "1",
			bodyPayload: func() updateEmployeeParams {
				p := validParams
				p.PositionIDs = []int64{-1}
				return p
			}(),
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name:        "TS-HDL-23: Unknown id maps ErrEmployeeNotFound to 404",
			idParam:     "999",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdateEmployee(gomock.Any(), int64(999), validParams).
					Return(EmployeeDetail{}, ErrEmployeeNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:        "TS-HDL-24: Map conflict error when email already exists in service",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdateEmployee(gomock.Any(), int64(1), validParams).
					Return(EmployeeDetail{}, ErrEmailAlreadyExists)
			},
			expectedCode: http.StatusConflict,
		},
		{
			name:        "TS-HDL-24b: Map conflict error when username already exists in service",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdateEmployee(gomock.Any(), int64(1), validParams).
					Return(EmployeeDetail{}, ErrUsernameAlreadyExists)
			},
			expectedCode: http.StatusConflict,
		},
		{
			name:        "TS-HDL-24c: Map conflict error when employee_id already exists in service",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdateEmployee(gomock.Any(), int64(1), validParams).
					Return(EmployeeDetail{}, ErrOdooEmployeeIDAlreadyExists)
			},
			expectedCode: http.StatusConflict,
		},
		{
			name:        "TS-HDL-24d: Map unknown position id to 400",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdateEmployee(gomock.Any(), int64(1), validParams).
					Return(EmployeeDetail{}, ErrUnknownPositionID)
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-24e: Map unverified odoo employee id to 400",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdateEmployee(gomock.Any(), int64(1), validParams).
					Return(EmployeeDetail{}, ErrOdooEmployeeIDNotFound)
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-25: Map internal server error on database failure",
			idParam:     "1",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					UpdateEmployee(gomock.Any(), int64(1), validParams).
					Return(EmployeeDetail{}, errors.New("connection refused"))
			},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:    "TS-HDL-26: Update employee successfully with password included",
			idParam: "1",
			bodyPayload: func() updateEmployeeParams {
				p := validParams
				pw := "supersecret"
				p.Password = &pw
				return p
			}(),
			setupMock: func(mockSvc *MockService) {
				pw := "supersecret"
				withPassword := validParams
				withPassword.Password = &pw
				mockSvc.EXPECT().
					UpdateEmployee(gomock.Any(), int64(1), withPassword).
					Return(EmployeeDetail{Employee: repo.Employee{ID: 1, FullName: validParams.FullName, Email: validParams.Email, Username: validParams.Username}}, nil)
			},
			expectedCode: http.StatusOK,
		},
		{
			name:    "TS-HDL-27: Password shorter than 8 chars fails validation",
			idParam: "1",
			bodyPayload: func() updateEmployeeParams {
				p := validParams
				pw := "short"
				p.Password = &pw
				return p
			}(),
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
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
			req := httptest.NewRequest(http.MethodPut, "/employees/"+tc.idParam, bytes.NewReader(jsonBody))
			req = withURLParam(req, "id", tc.idParam)
			rec := httptest.NewRecorder()

			h.UpdateEmployee(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestEmployeeHandler_SetEmployeeActive(t *testing.T) {
	tests := []struct {
		name          string
		idParam       string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "TS-HDL-26: Deactivate an existing employee",
			idParam:     "1",
			bodyPayload: setEmployeeActiveParams{IsActive: boolPtr(false)},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetEmployeeActive(gomock.Any(), int64(1), false).
					Return(nil)
			},
			expectedCode: http.StatusNoContent,
		},
		{
			name:        "TS-HDL-27: Activate an existing employee",
			idParam:     "1",
			bodyPayload: setEmployeeActiveParams{IsActive: boolPtr(true)},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetEmployeeActive(gomock.Any(), int64(1), true).
					Return(nil)
			},
			expectedCode: http.StatusNoContent,
		},
		{
			name:        "TS-HDL-28: Non-numeric id path param returns 400",
			idParam:     "not-a-number",
			bodyPayload: setEmployeeActiveParams{IsActive: boolPtr(true)},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because parsing fails at the handler layer
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-28b: Zero id path param returns 400",
			idParam:     "0",
			bodyPayload: setEmployeeActiveParams{IsActive: boolPtr(true)},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-28c: Negative id path param returns 400",
			idParam:     "-1",
			bodyPayload: setEmployeeActiveParams{IsActive: boolPtr(true)},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-29: Unknown id maps ErrEmployeeNotFound to 404",
			idParam:     "999",
			bodyPayload: setEmployeeActiveParams{IsActive: boolPtr(true)},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetEmployeeActive(gomock.Any(), int64(999), true).
					Return(ErrEmployeeNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:        "TS-HDL-30: Map internal server error on database failure",
			idParam:     "1",
			bodyPayload: setEmployeeActiveParams{IsActive: boolPtr(true)},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetEmployeeActive(gomock.Any(), int64(1), true).
					Return(errors.New("connection refused"))
			},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:        "TS-HDL-30b: Missing isActive field returns 400 instead of defaulting to false",
			idParam:     "1",
			bodyPayload: struct{}{},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
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
			req := httptest.NewRequest(http.MethodPatch, "/employees/"+tc.idParam+"/status", bytes.NewReader(jsonBody))
			req = withURLParam(req, "id", tc.idParam)
			rec := httptest.NewRecorder()

			h.SetEmployeeActive(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestEmployeeHandler_SetEmployeePassword(t *testing.T) {
	tests := []struct {
		name          string
		idParam       string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "TS-HDL-31: Admin sets a new password for an existing employee",
			idParam:     "1",
			bodyPayload: setEmployeePasswordParams{Password: "supersecret"},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetEmployeePassword(gomock.Any(), int64(1), "supersecret").
					Return(nil)
			},
			expectedCode: http.StatusNoContent,
		},
		{
			name:        "TS-HDL-32: Non-numeric id path param returns 400",
			idParam:     "not-a-number",
			bodyPayload: setEmployeePasswordParams{Password: "supersecret"},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because parsing fails at the handler layer
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-32b: Zero id path param returns 400",
			idParam:     "0",
			bodyPayload: setEmployeePasswordParams{Password: "supersecret"},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-32c: Negative id path param returns 400",
			idParam:     "-1",
			bodyPayload: setEmployeePasswordParams{Password: "supersecret"},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-33: Password shorter than 8 chars fails validation",
			idParam:     "1",
			bodyPayload: setEmployeePasswordParams{Password: "short"},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name:        "TS-HDL-34: Unknown id maps ErrEmployeeNotFound to 404",
			idParam:     "999",
			bodyPayload: setEmployeePasswordParams{Password: "supersecret"},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetEmployeePassword(gomock.Any(), int64(999), "supersecret").
					Return(ErrEmployeeNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:        "TS-HDL-35: Map internal server error on database failure",
			idParam:     "1",
			bodyPayload: setEmployeePasswordParams{Password: "supersecret"},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SetEmployeePassword(gomock.Any(), int64(1), "supersecret").
					Return(errors.New("connection refused"))
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
			req := httptest.NewRequest(http.MethodPatch, "/employees/"+tc.idParam+"/password", bytes.NewReader(jsonBody))
			req = withURLParam(req, "id", tc.idParam)
			rec := httptest.NewRecorder()

			h.SetEmployeePassword(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestEmployeeHandler_DeleteEmployee(t *testing.T) {
	tests := []struct {
		name         string
		idParam      string
		setupMock    func(mockSvc *MockService)
		expectedCode int
	}{
		{
			name:    "TS-HDL-31: Delete an existing employee",
			idParam: "1",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					DeleteEmployee(gomock.Any(), int64(1)).
					Return(nil)
			},
			expectedCode: http.StatusNoContent,
		},
		{
			name:    "TS-HDL-32: Non-numeric id path param returns 400",
			idParam: "not-a-number",
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because parsing fails at the handler layer
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "TS-HDL-32b: Zero id path param returns 400",
			idParam: "0",
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "TS-HDL-32c: Negative id path param returns 400",
			idParam: "-1",
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:    "TS-HDL-33: Unknown id maps ErrEmployeeNotFound to 404",
			idParam: "999",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					DeleteEmployee(gomock.Any(), int64(999)).
					Return(ErrEmployeeNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:    "TS-HDL-34: Map internal server error on database failure",
			idParam: "1",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					DeleteEmployee(gomock.Any(), int64(1)).
					Return(errors.New("connection refused"))
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

			req := httptest.NewRequest(http.MethodDelete, "/employees/"+tc.idParam, nil)
			req = withURLParam(req, "id", tc.idParam)
			rec := httptest.NewRecorder()

			h.DeleteEmployee(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}
		})
	}
}

func TestEmployeeHandler_BulkDeleteEmployees(t *testing.T) {
	tests := []struct {
		name          string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "TS-HDL-35: Bulk delete with mixed results",
			bodyPayload: bulkDeleteEmployeesParams{IDs: []int64{1, 999}},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					BulkDeleteEmployees(gomock.Any(), []int64{1, 999}).
					Return([]BulkActionResult{
						{ID: 1, Success: true},
						{ID: 999, Success: false, Error: ErrEmployeeNotFound.Error()},
					})
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got []BulkActionResult
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if len(got) != 2 {
					t.Fatalf("expected 2 results, got %d", len(got))
				}
				if got[1].Success {
					t.Errorf("expected id 999 to fail, got success=true")
				}
			},
		},
		{
			name:        "TS-HDL-36: Empty ids list returns 400",
			bodyPayload: bulkDeleteEmployeesParams{IDs: []int64{}},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
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
			req := httptest.NewRequest(http.MethodDelete, "/employees", bytes.NewReader(jsonBody))
			rec := httptest.NewRecorder()

			h.BulkDeleteEmployees(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestEmployeeHandler_BulkSendPasswordResetLinks(t *testing.T) {
	tests := []struct {
		name          string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "TS-HDL-37: Bulk send with mixed results",
			bodyPayload: bulkSendPasswordResetLinksParams{IDs: []int64{1, 2}},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					BulkSendPasswordResetLinks(gomock.Any(), []int64{1, 2}).
					Return([]BulkActionResult{
						{ID: 1, Success: true},
						{ID: 2, Success: false, Error: ErrEmployeeNotActive.Error()},
					})
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got []BulkActionResult
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if len(got) != 2 {
					t.Fatalf("expected 2 results, got %d", len(got))
				}
				if got[1].Success {
					t.Errorf("expected id 2 to fail, got success=true")
				}
			},
		},
		{
			name:        "TS-HDL-38: Empty ids list returns 400",
			bodyPayload: bulkSendPasswordResetLinksParams{IDs: []int64{}},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name:        "TS-HDL-39: Ids list over the batch limit returns 400",
			bodyPayload: bulkSendPasswordResetLinksParams{IDs: make([]int64, 101)},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
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
			req := httptest.NewRequest(http.MethodPost, "/employees/password-reset-links", bytes.NewReader(jsonBody))
			rec := httptest.NewRecorder()

			h.BulkSendPasswordResetLinks(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestEmployeeHandler_CompleteActivation(t *testing.T) {
	validParams := completeActivationParams{Token: "sometoken", Password: "supersecret", ConfirmPassword: "supersecret"}

	tests := []struct {
		name          string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "TS-HDL-39: Completes activation successfully",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().CompleteActivation(gomock.Any(), validParams).Return(nil)
			},
			expectedCode: http.StatusNoContent,
		},
		{
			name:        "TS-HDL-40: Missing/short fields return 400",
			bodyPayload: completeActivationParams{Token: "sometoken", Password: "short", ConfirmPassword: "short"},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name:        "TS-HDL-41: Invalid or expired token maps to 400",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().CompleteActivation(gomock.Any(), validParams).Return(ErrInvalidOrExpiredToken)
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-42: Map internal server error on database failure",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().CompleteActivation(gomock.Any(), validParams).Return(errors.New("connection refused"))
			},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:        "TS-HDL-43: confirmPassword mismatch returns 400 without calling the service",
			bodyPayload: completeActivationParams{Token: "sometoken", Password: "supersecret", ConfirmPassword: "different"},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called — eqfield validation rejects this at the Handler layer.
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
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
			req := httptest.NewRequest(http.MethodPost, "/activate", bytes.NewReader(jsonBody))
			rec := httptest.NewRecorder()

			h.CompleteActivation(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestEmployeeHandler_RequestPasswordReset(t *testing.T) {
	tests := []struct {
		name          string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "TS-HDL-44: Valid email always returns the generic 200 message",
			bodyPayload: requestPasswordResetParams{Email: "van-a@example.com"},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().RequestPasswordReset(gomock.Any(), "van-a@example.com")
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp requestPasswordResetResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if resp.Message != requestPasswordResetGenericMessage {
					t.Errorf("expected generic message %q, got %q", requestPasswordResetGenericMessage, resp.Message)
				}
			},
		},
		{
			name:        "TS-HDL-45: Malformed email returns 400 without calling the service",
			bodyPayload: requestPasswordResetParams{Email: "not-an-email"},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called — validation happens at the Handler layer.
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name:        "TS-HDL-46: Missing email returns 400 without calling the service",
			bodyPayload: requestPasswordResetParams{},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called — validation happens at the Handler layer.
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-47: Unknown/inactive/pending-activation employees still get the generic 200 (anti-enumeration)",
			bodyPayload: requestPasswordResetParams{Email: "unknown@example.com"},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().RequestPasswordReset(gomock.Any(), "unknown@example.com")
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var resp requestPasswordResetResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if resp.Message != requestPasswordResetGenericMessage {
					t.Errorf("expected generic message %q, got %q", requestPasswordResetGenericMessage, resp.Message)
				}
			},
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
			req := httptest.NewRequest(http.MethodPost, "/password-reset-requests", bytes.NewReader(jsonBody))
			rec := httptest.NewRecorder()

			h.RequestPasswordReset(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestEmployeeHandler_ListEmployees(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name: "TS-HDL-13: List employees successfully",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					ListEmployees(gomock.Any(), gomock.Any()).
					Return([]EmployeeDetail{
						{Employee: repo.Employee{ID: 1, OdooEmployeeID: 30, FullName: "Nguyen Van A"}},
						{Employee: repo.Employee{ID: 2, OdooEmployeeID: 31, FullName: "Tran Thi B"}},
					}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got []repo.Employee
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if len(got) != 2 {
					t.Errorf("expected 2 employees, got %d", len(got))
				}
			},
		},
		{
			name: "TS-HDL-14: List employees returns an empty array when there are none",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					ListEmployees(gomock.Any(), gomock.Any()).
					Return([]EmployeeDetail{}, nil)
			},
			expectedCode: http.StatusOK,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got []repo.Employee
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if len(got) != 0 {
					t.Errorf("expected 0 employees, got %d", len(got))
				}
			},
		},
		{
			name: "TS-HDL-15: Map internal server error on database failure",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					ListEmployees(gomock.Any(), gomock.Any()).
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

			req := httptest.NewRequest(http.MethodGet, "/employees", nil)
			rec := httptest.NewRecorder()

			h.ListEmployees(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestEmployeeHandler_CreateEmployee(t *testing.T) {
	// Default valid payload from the client
	validParams := createEmployeeParams{
		OdooEmployeeID: 30,
		FullName:       "Nguyen Van A",
		Email:          "van-a@example.com",
		Username:       "nguyenvana",
	}

	tests := []struct {
		name          string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "TS-HDL-01: Create employee successfully with valid input",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreateEmployee(gomock.Any(), validParams).
					Return(EmployeeDetail{Employee: repo.Employee{
						ID:             1,
						OdooEmployeeID: validParams.OdooEmployeeID,
						FullName:       validParams.FullName,
						Email:          validParams.Email,
						Username:       validParams.Username,
					}}, nil)
			},
			expectedCode: http.StatusCreated,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got repo.Employee
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.ID != 1 {
					t.Errorf("expected employee ID 1, got %d", got.ID)
				}
			},
		},
		{
			name: "TS-HDL-02: Create employee failed due to missing required fields",
			bodyPayload: createEmployeeParams{
				OdooEmployeeID: 0, // missing required field
				Email:          "invalid-email",
			},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name: "TS-HDL-02b: Negative position id in body fails validation",
			bodyPayload: func() createEmployeeParams {
				p := validParams
				p.PositionIDs = []int64{-1}
				return p
			}(),
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name:        "TS-HDL-04: Map conflict error when email already exists in service",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreateEmployee(gomock.Any(), validParams).
					Return(EmployeeDetail{}, ErrEmailAlreadyExists)
			},
			expectedCode: http.StatusConflict,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("email already exists")) {
					t.Errorf("expected response to mention email already exists, got %q", rec.Body.String())
				}
			},
		},
		{
			name:        "TS-HDL-04b: Map conflict error when odoo employee ID already exists",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreateEmployee(gomock.Any(), validParams).
					Return(EmployeeDetail{}, ErrOdooEmployeeIDAlreadyExists)
			},
			expectedCode: http.StatusConflict,
		},
		{
			name:        "TS-HDL-04c: Map conflict error when username already exists",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreateEmployee(gomock.Any(), validParams).
					Return(EmployeeDetail{}, ErrUsernameAlreadyExists)
			},
			expectedCode: http.StatusConflict,
		},
		{
			name:        "TS-HDL-04e: Map unverified odoo employee id to 400",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreateEmployee(gomock.Any(), validParams).
					Return(EmployeeDetail{}, ErrOdooEmployeeIDNotFound)
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-04d: Map unknown position id to 400",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreateEmployee(gomock.Any(), validParams).
					Return(EmployeeDetail{}, ErrUnknownPositionID)
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name:        "TS-HDL-12: Map internal server error on database failure",
			bodyPayload: validParams,
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					CreateEmployee(gomock.Any(), validParams).
					Return(EmployeeDetail{}, errors.New("connection refused"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 1. Setup Mock Controller and Service
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)

			tc.setupMock(mockSvc)

			// 2. Initialize Handler with Mocked Service
			h := NewHandler(mockSvc)

			// 3. Setup HTTP Request and Recorder
			jsonBody, err := json.Marshal(tc.bodyPayload)
			if err != nil {
				t.Fatalf("failed to marshal request body: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/employees", bytes.NewReader(jsonBody))
			rec := httptest.NewRecorder()

			// 4. Execute the Handler method directly
			h.CreateEmployee(rec, req)

			// 5. Assert HTTP Status Code
			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			// 6. Assert Custom Response Conditions if any
			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

// TestEmployeeHandler_ResponseOmitsPasswordHash asserts that the bcrypt
// password hash never reaches a client: repo.Employee.Password is tagged
// json:"password" with no omitempty, so json.Write(w, ..., employee) would
// otherwise serialize it (base64-encoded, since it's []byte) in every
// response that echoes an employee back.
func TestEmployeeHandler_ResponseOmitsPasswordHash(t *testing.T) {
	hashedPassword := []byte("$2a$10$abcdefghijklmnopqrstuv")
	employeeWithHash := repo.Employee{
		ID:             1,
		OdooEmployeeID: 30,
		FullName:       "Nguyen Van A",
		Email:          "van-a@example.com",
		Username:       "nguyenvana",
		Password:       hashedPassword,
		IsActive:       true,
	}
	detailWithHash := EmployeeDetail{Employee: employeeWithHash}

	assertNoPassword := func(t *testing.T, body []byte) {
		t.Helper()
		if bytes.Contains(body, hashedPassword) {
			t.Errorf("response body contains the raw password hash: %s", body)
		}
		if bytes.Contains(body, []byte(`"password"`)) {
			t.Errorf("response body has a \"password\" key: %s", body)
		}
	}

	t.Run("CreateEmployee", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockSvc := NewMockService(ctrl)
		params := createEmployeeParams{OdooEmployeeID: 30, FullName: "Nguyen Van A", Email: "van-a@example.com", Username: "nguyenvana"}
		mockSvc.EXPECT().CreateEmployee(gomock.Any(), params).Return(detailWithHash, nil)

		h := NewHandler(mockSvc)
		jsonBody, _ := json.Marshal(params)
		req := httptest.NewRequest(http.MethodPost, "/employees", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		h.CreateEmployee(rec, req)

		assertNoPassword(t, rec.Body.Bytes())
	})

	t.Run("GetEmployeeByID", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockSvc := NewMockService(ctrl)
		mockSvc.EXPECT().GetEmployeeByID(gomock.Any(), int64(1)).Return(detailWithHash, nil)

		h := NewHandler(mockSvc)
		req := httptest.NewRequest(http.MethodGet, "/employees/1", nil)
		req = withURLParam(req, "id", "1")
		rec := httptest.NewRecorder()

		h.GetEmployeeByID(rec, req)

		assertNoPassword(t, rec.Body.Bytes())
	})

	t.Run("UpdateEmployee", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockSvc := NewMockService(ctrl)
		params := updateEmployeeParams{OdooEmployeeID: 30, FullName: "Nguyen Van A", Email: "van-a@example.com", Username: "nguyenvana"}
		mockSvc.EXPECT().UpdateEmployee(gomock.Any(), int64(1), params).Return(detailWithHash, nil)

		h := NewHandler(mockSvc)
		jsonBody, _ := json.Marshal(params)
		req := httptest.NewRequest(http.MethodPut, "/employees/1", bytes.NewReader(jsonBody))
		req = withURLParam(req, "id", "1")
		rec := httptest.NewRecorder()

		h.UpdateEmployee(rec, req)

		assertNoPassword(t, rec.Body.Bytes())
	})

	t.Run("ListEmployees", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockSvc := NewMockService(ctrl)
		mockSvc.EXPECT().ListEmployees(gomock.Any(), gomock.Any()).Return([]EmployeeDetail{detailWithHash}, nil)

		h := NewHandler(mockSvc)
		req := httptest.NewRequest(http.MethodGet, "/employees", nil)
		rec := httptest.NewRecorder()

		h.ListEmployees(rec, req)

		assertNoPassword(t, rec.Body.Bytes())
	})
}

func TestEmployeeHandler_SyncEmployees(t *testing.T) {
	tests := []struct {
		name          string
		bodyPayload   any
		setupMock     func(mockSvc *MockService)
		expectedCode  int
		checkResponse func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:        "TS-HDL-40: Starts a sync and returns 202 immediately",
			bodyPayload: syncEmployeesParams{IDs: []int64{1, 2}},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SyncEmployees(gomock.Any(), []int64{1, 2}).
					Return(nil)
			},
			expectedCode: http.StatusAccepted,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				var got syncEmployeesResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("failed to unmarshal response body: %v", err)
				}
				if got.Status != "accepted" {
					t.Errorf("expected status %q, got %q", "accepted", got.Status)
				}
			},
		},
		{
			name:        "TS-HDL-41: Empty ids list returns 400",
			bodyPayload: syncEmployeesParams{IDs: []int64{}},
			setupMock: func(mockSvc *MockService) {
				// Service should NOT be called because validation happens at the Handler layer
			},
			expectedCode: http.StatusBadRequest,
			checkResponse: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if !bytes.Contains(rec.Body.Bytes(), []byte("validation")) {
					t.Errorf("expected response to mention validation, got %q", rec.Body.String())
				}
			},
		},
		{
			name:        "TS-HDL-42: Ids list over 50 is accepted — the service pages through it internally",
			bodyPayload: syncEmployeesParams{IDs: make([]int64, 51)},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SyncEmployees(gomock.Any(), make([]int64, 51)).
					Return(nil)
			},
			expectedCode: http.StatusAccepted,
		},
		{
			name:        "TS-HDL-43: A sync already in progress maps ErrSyncInProgress to 409",
			bodyPayload: syncEmployeesParams{IDs: []int64{1}},
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().
					SyncEmployees(gomock.Any(), []int64{1}).
					Return(ErrSyncInProgress)
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
			req := httptest.NewRequest(http.MethodPost, "/employees/syncs", bytes.NewReader(jsonBody))
			rec := httptest.NewRecorder()

			h.SyncEmployees(rec, req)

			if rec.Code != tc.expectedCode {
				t.Errorf("expected status %d, got %d", tc.expectedCode, rec.Code)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, rec)
			}
		})
	}
}

func TestEmployeeHandler_SyncStatus(t *testing.T) {
	tests := []struct {
		name      string
		setupMock func(mockSvc *MockService)
		want      SyncStatus
	}{
		{
			name: "TS-HDL-44: Reports syncing=true while a sync is in flight",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().SyncStatus(gomock.Any()).Return(SyncStatus{Syncing: true})
			},
			want: SyncStatus{Syncing: true},
		},
		{
			name: "TS-HDL-45: Reports syncing=false when idle",
			setupMock: func(mockSvc *MockService) {
				mockSvc.EXPECT().SyncStatus(gomock.Any()).Return(SyncStatus{Syncing: false})
			},
			want: SyncStatus{Syncing: false},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockSvc := NewMockService(ctrl)

			tc.setupMock(mockSvc)

			h := NewHandler(mockSvc)

			req := httptest.NewRequest(http.MethodGet, "/employees/syncs", nil)
			rec := httptest.NewRecorder()

			h.SyncStatus(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
			}

			var got SyncStatus
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("failed to unmarshal response body: %v", err)
			}
			if got != tc.want {
				t.Errorf("expected %+v, got %+v", tc.want, got)
			}
		})
	}
}
