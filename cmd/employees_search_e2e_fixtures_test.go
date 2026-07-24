//go:build dbe2e

// employeesSearchE2EFixtures is the Postgres-facing seam for
// cmd/employees_search_e2e_test.go (issue #28): creating Employee, Store,
// and Position rows and their employee_positions/employee_stores membership
// directly via repo.Querier, bypassing HTTP entirely — same reasoning as
// cmd/login_e2e_fixtures_test.go's Employee/Store (creating an Employee via
// the real CreateEmployee query never calls Odoo, keeping this suite
// Odoo-free the same way that one is). This suite needs its own fixtures
// type rather than reusing loginE2EFixtures because it needs direct control
// over FullName/Email/OdooEmployeeID/IsActive per Employee — exactly the
// fields GET /employees' filters key off — which loginE2EFixtures's
// Employee doesn't expose (it always derives FullName/Email from one
// generated username).
package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5/pgxpool"
)

type employeesSearchE2EFixtures struct {
	pool *pgxpool.Pool
	repo repo.Querier
}

func newEmployeesSearchE2EFixtures(pool *pgxpool.Pool, q repo.Querier) *employeesSearchE2EFixtures {
	return &employeesSearchE2EFixtures{pool: pool, repo: q}
}

// employeesSearchE2EEmployee is Employee's result: every id a test's filter
// query might need to reference.
type employeesSearchE2EEmployee struct {
	ID             int64
	OdooEmployeeID int64
}

// employeeSearchSeed configures Employee's fixture. FullName/Email default
// to a fresh e2eUnique value when left blank — set them explicitly to
// exercise q's full_name-vs-email matching. IsActive defaults to true
// (CreateEmployee's own DB default); set it to false to seed a deactivated
// Employee.
type employeeSearchSeed struct {
	FullName string
	Email    string
	IsActive *bool
}

// Employee inserts an Employee row via the real CreateEmployee query
// (exercising its own uniqueness constraints, same as
// cmd/login_e2e_fixtures_test.go's Employee), then applies seed.IsActive if
// it asks for a deactivated row — no query exists for setting is_active at
// creation time (SetEmployeeActive is a separate PATCH), so this reaches
// past CreateEmployee's result the same way that file's Employee does for
// activation state.
func (f *employeesSearchE2EFixtures) Employee(t *testing.T, seed employeeSearchSeed) employeesSearchE2EEmployee {
	t.Helper()
	ctx := t.Context()

	unique := e2eUnique(t, "search")
	fullName := seed.FullName
	if fullName == "" {
		fullName = "Search E2E " + unique
	}
	email := seed.Email
	if email == "" {
		email = unique + "@example.com"
	}

	employee, err := f.repo.CreateEmployee(ctx, repo.CreateEmployeeParams{
		OdooEmployeeID: employeesSearchE2ENextOdooID(),
		FullName:       fullName,
		Email:          email,
		Username:       unique,
	})
	if err != nil {
		t.Fatalf("seed employee: CreateEmployee: %v", err)
	}
	t.Cleanup(func() {
		if _, err := f.repo.DeleteEmployee(context.Background(), employee.ID); err != nil {
			t.Errorf("cleanup: delete employee %d: %v", employee.ID, err)
		}
	})

	if seed.IsActive != nil && !*seed.IsActive {
		if _, err := f.repo.SetEmployeeActive(ctx, repo.SetEmployeeActiveParams{ID: employee.ID, IsActive: false}); err != nil {
			t.Fatalf("seed employee: deactivate: %v", err)
		}
	}

	return employeesSearchE2EEmployee{ID: employee.ID, OdooEmployeeID: employee.OdooEmployeeID}
}

// Store inserts a bare Store row directly — no sqlc query creates a plain
// local Store outside of Odoo sync (ADR-0009), same as
// cmd/login_e2e_fixtures_test.go's Store.
func (f *employeesSearchE2EFixtures) Store(t *testing.T) int64 {
	t.Helper()
	ctx := t.Context()

	var id int64
	name := e2eUnique(t, "search-store")
	if err := f.pool.QueryRow(ctx, `INSERT INTO store (store_name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("seed store: insert: %v", err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `DELETE FROM store WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup: delete store %d: %v", id, err)
		}
	})
	return id
}

// Position creates a Position row via the real CreatePosition query.
func (f *employeesSearchE2EFixtures) Position(t *testing.T) int64 {
	t.Helper()
	ctx := t.Context()

	name := e2eUnique(t, "search-position")
	position, err := f.repo.CreatePosition(ctx, name)
	if err != nil {
		t.Fatalf("seed position: CreatePosition: %v", err)
	}
	t.Cleanup(func() {
		if _, err := f.repo.DeletePosition(context.Background(), position.ID); err != nil {
			t.Errorf("cleanup: delete position %d: %v", position.ID, err)
		}
	})
	return position.ID
}

// LinkStore attaches employeeID to storeID (employee_stores) — the facet
// store_ids filters against.
func (f *employeesSearchE2EFixtures) LinkStore(t *testing.T, employeeID, storeID int64) {
	t.Helper()
	if err := f.repo.InsertEmployeeStores(t.Context(), repo.InsertEmployeeStoresParams{
		EmployeeID: employeeID,
		StoreIds:   []int64{storeID},
	}); err != nil {
		t.Fatalf("link employee %d to store %d: %v", employeeID, storeID, err)
	}
}

// LinkPosition attaches employeeID to positionID (employee_positions) — the
// facet position_ids filters against.
func (f *employeesSearchE2EFixtures) LinkPosition(t *testing.T, employeeID, positionID int64) {
	t.Helper()
	if err := f.repo.InsertEmployeePositions(t.Context(), repo.InsertEmployeePositionsParams{
		EmployeeID:  employeeID,
		PositionIds: []int64{positionID},
	}); err != nil {
		t.Fatalf("link employee %d to position %d: %v", employeeID, positionID, err)
	}
}

// employeesSearchE2EOdooIDCounter hands out unique odoo_employee_id values
// (that column is NOT NULL UNIQUE, but otherwise meaningless to this suite)
// — seeded from the current time so re-runs across process restarts don't
// collide with rows a prior run failed to clean up (mirrors
// cmd/login_e2e_fixtures_test.go's loginE2EOdooIDCounter).
var employeesSearchE2EOdooIDCounter = time.Now().UnixNano()

func employeesSearchE2ENextOdooID() int64 {
	return atomic.AddInt64(&employeesSearchE2EOdooIDCounter, 1)
}
