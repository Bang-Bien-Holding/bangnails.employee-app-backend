package positions

import (
	"context"
	"errors"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// uniqueViolationCode is Postgres' SQLSTATE for a unique_violation error.
const uniqueViolationCode = "23505"

// foreignKeyViolationCode is Postgres' SQLSTATE for a foreign_key_violation
// error.
const foreignKeyViolationCode = "23503"

// positionsNameKeyConstraint comes from
// internal/adapters/postgresql/migrations/00010_create_positions.sql
// (Postgres' default naming: <table>_<column>_key).
const positionsNameKeyConstraint = "positions_name_key"

// employeePositionsPositionIDFkeyConstraint and
// employeePositionsEmployeeIDFkeyConstraint come from
// internal/adapters/postgresql/migrations/00011_create_employee_positions.sql
// (Postgres' default naming: <table>_<column>_fkey).
const (
	employeePositionsPositionIDFkeyConstraint = "employee_positions_position_id_fkey"
	employeePositionsEmployeeIDFkeyConstraint = "employee_positions_employee_id_fkey"
)

type service struct {
	// repo is a plain, non-transactional Querier for reads/writes that don't
	// need transaction scoping — everything except SetPositionEmployees'
	// delete+insert diff uses this rather than withTx.
	repo repo.Querier
	// withTx wraps fn in a transaction-scoped repo.Querier — a real
	// pool-backed implementation is installed by NewService; tests replace
	// it with a stub that calls fn against a mocked Querier directly.
	withTx func(ctx context.Context, fn func(repo.Querier) error) error
}

func NewService(pool *pgxpool.Pool) Service {
	return &service{
		repo: repo.New(pool),
		withTx: func(ctx context.Context, fn func(repo.Querier) error) error {
			tx, err := pool.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)

			if err := fn(repo.New(tx)); err != nil {
				return err
			}
			return tx.Commit(ctx)
		},
	}
}

func (s *service) CreatePosition(ctx context.Context, params createPositionParams) (repo.Position, error) {
	position, err := s.repo.CreatePosition(ctx, params.Name)
	if err != nil {
		return repo.Position{}, translatePositionUniqueViolation(err)
	}
	return position, nil
}

func (s *service) ListPositions(ctx context.Context) ([]repo.Position, error) {
	return s.repo.ListPositions(ctx)
}

func (s *service) UpdatePosition(ctx context.Context, id int64, params updatePositionParams) (repo.Position, error) {
	position, err := s.repo.UpdatePosition(ctx, repo.UpdatePositionParams{
		ID:   id,
		Name: params.Name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repo.Position{}, ErrPositionNotFound
		}
		return repo.Position{}, translatePositionUniqueViolation(err)
	}
	return position, nil
}

func (s *service) DeletePosition(ctx context.Context, id int64) error {
	rowsAffected, err := s.repo.DeletePosition(ctx, id)
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrPositionNotFound
	}
	return nil
}

// BulkDeletePositions deletes every submitted id in one transaction,
// all-or-nothing (see issue #13) — unlike employees.BulkDeleteEmployees'
// best-effort per-id []BulkActionResult, a Position bulk-delete is one
// intent from the FE's confirmation dialog, not a batch of independent
// attempts: if any id doesn't reference a real position, the whole request
// fails via ErrPositionNotFound and nothing is deleted.
func (s *service) BulkDeletePositions(ctx context.Context, ids []int64) error {
	return s.withTx(ctx, func(q repo.Querier) error {
		count, err := q.CountPositionsByIDs(ctx, ids)
		if err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return ErrPositionNotFound
		}
		_, err = q.DeletePositions(ctx, ids)
		return err
	})
}

func (s *service) GetPositionEmployees(ctx context.Context, id int64) ([]EmployeeDetail, error) {
	if _, err := s.repo.GetPositionByID(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPositionNotFound
		}
		return nil, err
	}

	employees, err := s.repo.ListEmployeesByPositionID(ctx, id)
	if err != nil {
		return nil, err
	}
	return buildEmployeeDetails(ctx, s.repo, employees)
}

// SetPositionEmployees replaces a position's whole employee set via diff
// (delete what's no longer submitted, insert what's newly submitted) inside
// one transaction, so a failing insert rolls back the delete too (see
// ADR-0011). It calls repo.Querier directly rather than employees.Service —
// a deliberate second writer of employee_positions, since a position-first
// diff (given a position, compute which employees to add/remove) is a
// different operation from employees.Service's employee-first diff, not the
// same operation duplicated.
func (s *service) SetPositionEmployees(ctx context.Context, id int64, params setPositionEmployeesParams) ([]EmployeeDetail, error) {
	employeeIDs := params.EmployeeIDs
	if employeeIDs == nil {
		employeeIDs = []int64{}
	}

	var details []EmployeeDetail
	err := s.withTx(ctx, func(q repo.Querier) error {
		if _, err := q.GetPositionByID(ctx, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPositionNotFound
			}
			return err
		}

		if err := validateEmployeeIDs(ctx, q, employeeIDs); err != nil {
			return err
		}

		if err := q.DeleteEmployeePositionsByPositionIDNotIn(ctx, repo.DeleteEmployeePositionsByPositionIDNotInParams{
			PositionID:  id,
			EmployeeIds: employeeIDs,
		}); err != nil {
			return err
		}
		if len(employeeIDs) > 0 {
			if err := q.InsertPositionEmployees(ctx, repo.InsertPositionEmployeesParams{
				PositionID:  id,
				EmployeeIds: employeeIDs,
			}); err != nil {
				return translateInsertPositionEmployeesForeignKeyViolation(err)
			}
		}

		employees, err := q.ListEmployeesByPositionID(ctx, id)
		if err != nil {
			return err
		}
		details, err = buildEmployeeDetails(ctx, q, employees)
		return err
	})
	if err != nil {
		return nil, err
	}

	return details, nil
}

// buildEmployeeDetails attaches each employee's full position/store id sets
// (the same batched-by-ids queries employees.Service.ListEmployees uses),
// for GetPositionEmployees/SetPositionEmployees' employeeResponse shape (see
// issue #13). Called with a plain repo.Querier for reads and with the
// transaction-scoped Querier from SetPositionEmployees' withTx so the
// refetch sees its own just-written diff.
func buildEmployeeDetails(ctx context.Context, q repo.Querier, employees []repo.Employee) ([]EmployeeDetail, error) {
	ids := make([]int64, len(employees))
	for i, e := range employees {
		ids[i] = e.ID
	}

	positionsByEmployee, err := q.ListPositionIDsByEmployeeIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	positionIDs := make(map[int64][]int64, len(employees))
	for _, p := range positionsByEmployee {
		positionIDs[p.EmployeeID] = append(positionIDs[p.EmployeeID], p.PositionID)
	}

	storesByEmployee, err := q.ListStoreIDsByEmployeeIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	storeIDs := make(map[int64][]int64, len(employees))
	for _, st := range storesByEmployee {
		storeIDs[st.EmployeeID] = append(storeIDs[st.EmployeeID], st.StoreID)
	}

	details := make([]EmployeeDetail, len(employees))
	for i, e := range employees {
		positions := positionIDs[e.ID]
		if positions == nil {
			positions = []int64{}
		}
		stores := storeIDs[e.ID]
		if stores == nil {
			stores = []int64{}
		}
		details[i] = EmployeeDetail{Employee: e, PositionIDs: positions, StoreIDs: stores}
	}
	return details, nil
}

// validateEmployeeIDs rejects a submitted employee-id set containing an id
// that isn't a real employee, via one round trip comparing CountEmployeesByIDs
// against the distinct submitted count (see ADR-0011, mirroring
// employees.validatePositionIDs). An empty/nil ids is always valid (a
// position with no employees), so it short-circuits before the query.
func validateEmployeeIDs(ctx context.Context, q repo.Querier, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	count, err := q.CountEmployeesByIDs(ctx, ids)
	if err != nil {
		return err
	}
	if count != int64(len(ids)) {
		return ErrUnknownEmployeeID
	}
	return nil
}

// translatePositionUniqueViolation maps a Postgres unique-violation on
// positions.name to ErrPositionNameAlreadyExists, leaving every other error
// untouched. Shared by CreatePosition and UpdatePosition.
func translatePositionUniqueViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolationCode {
		return err
	}
	if pgErr.ConstraintName == positionsNameKeyConstraint {
		return ErrPositionNameAlreadyExists
	}
	return err
}

// translateInsertPositionEmployeesForeignKeyViolation maps a Postgres
// foreign-key violation on employee_positions to the matching domain error —
// ErrPositionNotFound if the position was deleted, ErrUnknownEmployeeID if an
// employee was deleted — out of the narrow race window between
// SetPositionEmployees' pre-checks and this insert, leaving every other
// error untouched.
func translateInsertPositionEmployeesForeignKeyViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != foreignKeyViolationCode {
		return err
	}
	switch pgErr.ConstraintName {
	case employeePositionsPositionIDFkeyConstraint:
		return ErrPositionNotFound
	case employeePositionsEmployeeIDFkeyConstraint:
		return ErrUnknownEmployeeID
	default:
		return err
	}
}
