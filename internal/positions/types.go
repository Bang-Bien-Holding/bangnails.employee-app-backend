package positions

//go:generate mockgen -source=types.go -destination=service_mock_test.go -package=positions

import (
	"context"
	"errors"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrPositionNotFound is returned by UpdatePosition/DeletePosition for an id
// with no matching row.
var ErrPositionNotFound = errors.New("position not found")

// ErrPositionNameAlreadyExists is returned by CreatePosition/UpdatePosition
// when name collides with an existing position (see ADR-0008 — this must
// surface as a clear client error, not a raw database constraint failure).
var ErrPositionNameAlreadyExists = errors.New("position name already exists")

// ErrUnknownEmployeeID is returned by SetPositionEmployees when employeeIds
// references an id that isn't a real employee — a clear client error, not a
// raw FK-violation 500 (see ADR-0011, mirroring employees.ErrUnknownPositionID).
var ErrUnknownEmployeeID = errors.New("unknown employee id")

// createPositionParams is the body for POST /v1/positions.
type createPositionParams struct {
	Name string `json:"name" validate:"required"`
}

// updatePositionParams is the body for PUT /v1/positions/{id} — Position
// has exactly one field, so a rename is the whole resource.
type updatePositionParams struct {
	Name string `json:"name" validate:"required"`
}

// positionResponse mirrors repo.Position for HTTP responses.
type positionResponse struct {
	ID        int64              `json:"id"`
	Name      string             `json:"name"`
	CreatedAt pgtype.Timestamptz `json:"created_at"`
	UpdatedAt pgtype.Timestamptz `json:"updated_at"`
}

func newPositionResponse(p repo.Position) positionResponse {
	return positionResponse{
		ID:        p.ID,
		Name:      p.Name,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

func newPositionResponses(positions []repo.Position) []positionResponse {
	responses := make([]positionResponse, len(positions))
	for i, p := range positions {
		responses[i] = newPositionResponse(p)
	}
	return responses
}

// setPositionEmployeesParams is the body for PUT /v1/positions/{id}/employees
// (ADR-0011) — always a whole-set replace via diff, same always-full-replace
// convention as createEmployeeParams.PositionIDs: a nil/empty EmployeeIDs
// both mean "no employees assigned".
type setPositionEmployeesParams struct {
	EmployeeIDs []int64 `json:"employeeIds" validate:"omitempty,unique,dive,required"`
}

// bulkDeletePositionsParams is the body for DELETE /v1/positions (bulk,
// issue #13) — validated strictly (non-empty, unique, every id positive)
// rather than employees.bulkDeleteEmployeesParams' looser
// "required,min=1": BulkDeletePositions is all-or-nothing, so a malformed
// id belongs in a 400 before the database is ever touched.
type bulkDeletePositionsParams struct {
	IDs []int64 `json:"ids" validate:"required,min=1,unique,dive,gt=0"`
}

// EmployeeDetail is one employee assigned to a position, plus its full
// position/store id sets — the position-first counterpart of
// employees.EmployeeDetail. GetPositionEmployees/SetPositionEmployees build
// this straight from repo.Querier rather than importing the employees
// package, per ADR-0011's "positions is a second, deliberate reader/writer
// of employee_positions" precedent.
type EmployeeDetail struct {
	Employee    repo.Employee
	PositionIDs []int64
	StoreIDs    []int64
}

// employeeResponse mirrors employees.employeeResponse's shape field-for-field
// (see issue #13 — GET/PUT /positions/{id}/employees reuse the exact
// response shape GET /employees already returns, not a new one). Duplicated
// here rather than imported to keep positions decoupled from employees'
// internals, minus Password for the same reason employees.employeeResponse
// excludes it.
type employeeResponse struct {
	ID             int64              `json:"id"`
	OdooEmployeeID int64              `json:"odoo_employee_id"`
	FullName       string             `json:"full_name"`
	Email          string             `json:"email"`
	Username       string             `json:"username"`
	IsActive       bool               `json:"is_active"`
	PositionIDs    []int64            `json:"position_ids"`
	StoreIDs       []int64            `json:"store_ids"`
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
		StoreIDs:       d.StoreIDs,
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
	CreatePosition(ctx context.Context, params createPositionParams) (repo.Position, error)
	ListPositions(ctx context.Context) ([]repo.Position, error)
	UpdatePosition(ctx context.Context, id int64, params updatePositionParams) (repo.Position, error)
	DeletePosition(ctx context.Context, id int64) error
	BulkDeletePositions(ctx context.Context, ids []int64) error
	GetPositionEmployees(ctx context.Context, id int64) ([]EmployeeDetail, error)
	SetPositionEmployees(ctx context.Context, id int64, params setPositionEmployeesParams) ([]EmployeeDetail, error)
}
