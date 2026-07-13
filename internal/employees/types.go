package employees

//go:generate mockgen -source=types.go -destination=service_mock_test.go -package=employees

import (
	"context"
	"errors"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrEmailAlreadyExists and ErrEmployeeIDAlreadyExists are sentinel conflict
// errors returned by Service.CreateEmployee — service.go translates the
// repo's Postgres unique-violation errors into these — so the HTTP handler
// can map known conflicts to 409.
var (
	ErrEmailAlreadyExists      = errors.New("email already exists")
	ErrEmployeeIDAlreadyExists = errors.New("employee ID already exists")
	ErrUsernameAlreadyExists   = errors.New("username already exists")
	ErrEmployeeNotFound        = errors.New("employee not found")
	// ErrEmployeeNotActive is returned by BulkSendPasswordResetLinks for a
	// deactivated employee — per the user's explicit choice, only active
	// employees are eligible to receive a password-set/reset link; a
	// deactivated employee must be reactivated first.
	ErrEmployeeNotActive = errors.New("employee is not active")
	// ErrInvalidOrExpiredToken is returned by CompleteActivation when the
	// token doesn't match RedeemPasswordResetToken's criteria (unknown,
	// already used, or past its expiry) — deliberately one generic error
	// rather than three distinct ones, so the public endpoint never reveals
	// which specific reason a token didn't work.
	ErrInvalidOrExpiredToken = errors.New("invalid or expired token")
	// ErrSyncInProgress is returned by SyncEmployees when a previous call's
	// background sync hasn't finished yet — only one runs at a time.
	ErrSyncInProgress = errors.New("employee sync already in progress")
)

type createEmployeeParams struct {
	EmployeeID string `json:"employeeId" validate:"required"`
	FullName   string `json:"fullName" validate:"required"`
	Email      string `json:"email" validate:"required,email"`
	Username   string `json:"username" validate:"required"`
	Role       string `json:"role" validate:"required"`
}

// updateEmployeeParams.Password is optional (a *string, unlike
// setEmployeePasswordParams.Password) — PUT /employees/{id} updates the
// other fields unconditionally, but a nil Password leaves the existing
// password untouched rather than requiring every update to resupply it.
type updateEmployeeParams struct {
	EmployeeID string  `json:"employeeId" validate:"required"`
	FullName   string  `json:"fullName" validate:"required"`
	Email      string  `json:"email" validate:"required,email"`
	Username   string  `json:"username" validate:"required"`
	Role       string  `json:"role" validate:"required"`
	Password   *string `json:"password,omitempty" validate:"omitempty,min=8"`
}

// setEmployeePasswordParams is the body for PATCH /employees/{id}/password —
// an admin directly assigns the employee's new password, distinct from the
// token/email-based flow in completeActivationParams where the employee
// sets their own password.
type setEmployeePasswordParams struct {
	Password string `json:"password" validate:"required,min=8"`
}

// setEmployeeActiveParams is the body for PATCH /employees/{id}/status —
// "active status" is treated as its own sub-resource rather than
// verb-style /activate, /deactivate endpoints. IsActive is a pointer so
// validate:"required" can reject an omitted field instead of silently
// defaulting to false.
type setEmployeeActiveParams struct {
	IsActive *bool `json:"isActive" validate:"required"`
}

// bulkDeleteEmployeesParams is the body for DELETE /employees (bulk).
type bulkDeleteEmployeesParams struct {
	IDs []int64 `json:"ids" validate:"required,min=1"`
}

// BulkActionResult reports the outcome of one employee within a bulk
// request (delete, send password-reset link, ...) — best-effort per the
// user's explicit choice: one unknown/failing id doesn't roll back or
// block the others.
type BulkActionResult struct {
	ID      int64  `json:"id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// bulkSendPasswordResetLinksParams is the body for
// POST /employees/password-reset-links. max=100 bounds one request to a
// sane batch size — BulkSendPasswordResetLinks sends each mail
// synchronously and sequentially, so an unbounded list ties up the request
// for arbitrarily long.
type bulkSendPasswordResetLinksParams struct {
	IDs []int64 `json:"ids" validate:"required,min=1,max=100"`
}

// completeActivationParams is the body for the public POST /activate
// endpoint — serves both first-time activation and an admin-triggered
// password reset, since both land the employee on a page that consumes a
// password_reset_tokens row the same way.
type completeActivationParams struct {
	Token    string `json:"token" validate:"required"`
	Password string `json:"password" validate:"required,min=8"`
}

// syncEmployeesParams is the body for POST /employees/syncs. IDs are internal
// employees.id values (same convention as bulkDeleteEmployeesParams), not
// Odoo employee_ids — SyncEmployees looks up each one's employee_id before
// calling Odoo. There's no upper bound here — SyncEmployees paginates
// internally, fetching employeeSyncBatchSize ids from Odoo per round trip.
type syncEmployeesParams struct {
	IDs []int64 `json:"ids" validate:"required,min=1"`
}

// SyncStatus reports whether a SyncEmployees background job is currently
// running, for the frontend to poll and disable its trigger button
// accordingly.
type SyncStatus struct {
	Syncing bool `json:"syncing"`
}

// employeeResponse mirrors repo.Employee for HTTP responses, minus
// Password — repo.Employee.Password is tagged json:"password" with no
// omitempty (it's sqlc-generated, out of this package's control), so
// json.Write-ing a repo.Employee directly would serialize the bcrypt hash
// straight to the client. Handlers must convert via newEmployeeResponse
// instead of writing repo.Employee directly.
type employeeResponse struct {
	ID         int64              `json:"id"`
	EmployeeID string             `json:"employee_id"`
	FullName   string             `json:"full_name"`
	Email      string             `json:"email"`
	Username   string             `json:"username"`
	Role       string             `json:"role"`
	IsActive   bool               `json:"is_active"`
	CreatedAt  pgtype.Timestamptz `json:"created_at"`
	UpdatedAt  pgtype.Timestamptz `json:"updated_at"`
}

func newEmployeeResponse(e repo.Employee) employeeResponse {
	return employeeResponse{
		ID:         e.ID,
		EmployeeID: e.EmployeeID,
		FullName:   e.FullName,
		Email:      e.Email,
		Username:   e.Username,
		Role:       e.Role,
		IsActive:   e.IsActive,
		CreatedAt:  e.CreatedAt,
		UpdatedAt:  e.UpdatedAt,
	}
}

func newEmployeeResponses(employees []repo.Employee) []employeeResponse {
	responses := make([]employeeResponse, len(employees))
	for i, e := range employees {
		responses[i] = newEmployeeResponse(e)
	}
	return responses
}

type Service interface {
	CreateEmployee(ctx context.Context, params createEmployeeParams) (repo.Employee, error)
	ListEmployees(ctx context.Context) ([]repo.Employee, error)
	GetEmployeeByID(ctx context.Context, id int64) (repo.Employee, error)
	UpdateEmployee(ctx context.Context, id int64, params updateEmployeeParams) (repo.Employee, error)
	SetEmployeePassword(ctx context.Context, id int64, password string) error
	SetEmployeeActive(ctx context.Context, id int64, isActive bool) error
	DeleteEmployee(ctx context.Context, id int64) error
	BulkDeleteEmployees(ctx context.Context, ids []int64) []BulkActionResult
	BulkSendPasswordResetLinks(ctx context.Context, ids []int64) []BulkActionResult
	CompleteActivation(ctx context.Context, params completeActivationParams) error
	SyncEmployees(ctx context.Context, ids []int64) error
	SyncStatus(ctx context.Context) SyncStatus
}
