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
	ErrEmailAlreadyExists          = errors.New("email already exists")
	ErrOdooEmployeeIDAlreadyExists = errors.New("odoo employee ID already exists")
	ErrUsernameAlreadyExists       = errors.New("username already exists")
	ErrEmployeeNotFound            = errors.New("employee not found")
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
	// ErrUnknownPositionID is returned by CreateEmployee/UpdateEmployee when
	// positionIds references an id that isn't a real position — a clear
	// client error, not a raw FK-violation 500 (see ADR-0008).
	ErrUnknownPositionID = errors.New("unknown position id")
)

// createEmployeeParams.PositionIDs is optional and, when present, always
// replaces the employee's whole position set via diff (see ADR-0008) — a nil
// or empty slice both mean "no positions", matching every other field on
// this always-full-replace body.
type createEmployeeParams struct {
	OdooEmployeeID int64   `json:"odooEmployeeId" validate:"required"`
	FullName       string  `json:"fullName" validate:"required"`
	Email          string  `json:"email" validate:"required,email"`
	Username       string  `json:"username" validate:"required"`
	PositionIDs    []int64 `json:"positionIds" validate:"omitempty,unique,dive,required"`
}

// updateEmployeeParams.Password is optional (a *string, unlike
// setEmployeePasswordParams.Password) — PUT /employees/{id} updates the
// other fields unconditionally, but a nil Password leaves the existing
// password untouched rather than requiring every update to resupply it.
// PositionIDs follows createEmployeeParams' always-full-replace convention —
// see there.
type updateEmployeeParams struct {
	OdooEmployeeID int64   `json:"odooEmployeeId" validate:"required"`
	FullName       string  `json:"fullName" validate:"required"`
	Email          string  `json:"email" validate:"required,email"`
	Username       string  `json:"username" validate:"required"`
	Password       *string `json:"password,omitempty" validate:"omitempty,min=8"`
	PositionIDs    []int64 `json:"positionIds" validate:"omitempty,unique,dive,required"`
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
// calling Odoo. SyncEmployees paginates internally, fetching
// employeeSyncBatchSize ids from Odoo per round trip, but the request itself
// is still capped at max=100 (same bound as bulkSendPasswordResetLinksParams)
// so a single call can't force an unbounded ListEmployeeIDsByIDs lookup.
type syncEmployeesParams struct {
	IDs []int64 `json:"ids" validate:"required,min=1,max=100"`
}

// SyncStatus reports whether a SyncEmployees background job is currently
// running, for the frontend to poll and disable its trigger button
// accordingly.
type SyncStatus struct {
	Syncing bool `json:"syncing"`
}

// EmployeeDetail is the full picture of one employee: the employee row plus
// its current position assignments. PositionIDs is always non-nil (empty,
// not null, for an employee with no positions yet) — see stores.StoreDetail
// for the same convention with a store's wifi whitelist.
type EmployeeDetail struct {
	Employee    repo.Employee
	PositionIDs []int64
}

// employeeResponse mirrors repo.Employee (plus its position assignments)
// for HTTP responses, minus Password — repo.Employee.Password is tagged
// json:"password" with no omitempty (it's sqlc-generated, out of this
// package's control), so json.Write-ing a repo.Employee directly would
// serialize the bcrypt hash straight to the client. Handlers must convert
// via newEmployeeResponse instead of writing repo.Employee directly.
type employeeResponse struct {
	ID             int64              `json:"id"`
	OdooEmployeeID int64              `json:"odoo_employee_id"`
	FullName       string             `json:"full_name"`
	Email          string             `json:"email"`
	Username       string             `json:"username"`
	IsActive       bool               `json:"is_active"`
	PositionIDs    []int64            `json:"position_ids"`
	CreatedAt      pgtype.Timestamptz `json:"created_at"`
	UpdatedAt      pgtype.Timestamptz `json:"updated_at"`
}

func newEmployeeResponse(d EmployeeDetail) employeeResponse {
	return employeeResponse{
		ID:             d.Employee.ID,
		OdooEmployeeID: d.Employee.OdooEmployeeID,
		FullName:       d.Employee.FullName,
		Email:          d.Employee.Email,
		Username:       d.Employee.Username,
		IsActive:       d.Employee.IsActive,
		PositionIDs:    d.PositionIDs,
		CreatedAt:      d.Employee.CreatedAt,
		UpdatedAt:      d.Employee.UpdatedAt,
	}
}

func newEmployeeResponses(details []EmployeeDetail) []employeeResponse {
	responses := make([]employeeResponse, len(details))
	for i, d := range details {
		responses[i] = newEmployeeResponse(d)
	}
	return responses
}

type Service interface {
	CreateEmployee(ctx context.Context, params createEmployeeParams) (EmployeeDetail, error)
	ListEmployees(ctx context.Context) ([]EmployeeDetail, error)
	GetEmployeeByID(ctx context.Context, id int64) (EmployeeDetail, error)
	UpdateEmployee(ctx context.Context, id int64, params updateEmployeeParams) (EmployeeDetail, error)
	SetEmployeePassword(ctx context.Context, id int64, password string) error
	SetEmployeeActive(ctx context.Context, id int64, isActive bool) error
	DeleteEmployee(ctx context.Context, id int64) error
	BulkDeleteEmployees(ctx context.Context, ids []int64) []BulkActionResult
	BulkSendPasswordResetLinks(ctx context.Context, ids []int64) []BulkActionResult
	CompleteActivation(ctx context.Context, params completeActivationParams) error
	SyncEmployees(ctx context.Context, ids []int64) error
	SyncStatus(ctx context.Context) SyncStatus
}
