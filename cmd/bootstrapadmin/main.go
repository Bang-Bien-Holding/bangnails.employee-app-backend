// Command bootstrapadmin creates the very first Admin Employee (issue #26,
// ADR-0015). Every existing admin endpoint now requires an authenticated
// Admin Session (issue #25), but no Admin Session can exist before an Admin
// Employee does — this one-off script breaks that chicken-and-egg problem
// by writing directly to Postgres, bypassing both the HTTP API and the
// Odoo-employee-id existence check (ADR-0007) that CreateEmployee normally
// enforces. Run it once per environment; it is not a permanent code path or
// endpoint.
//
// Usage (reads DATABASE_DSN the same way cmd/api.go does, defaulting to the
// local dev database):
//
//	go run ./cmd/bootstrapadmin \
//	  -username admin -password change-me-immediately \
//	  -full-name "Admin Admin" -email admin@bangnails.local \
//	  -odoo-employee-id 1
//
// -odoo-employee-id is required because employees.odoo_employee_id is a
// NOT NULL UNIQUE column (see migration 00009) — this script skips the
// Odoo *existence check* CreateEmployee normally runs, not the column
// itself, so it must be given some id value (it does not need to
// correspond to a real hr.employee record for the account to work, since
// nothing here ever validates it against Odoo).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/env"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
)

// adminPositionName is the exact Position name ADR-0015 gates Login's
// presence-check bypass and admin-endpoint access on — kept in sync with
// auth's IsEmployeeAdmin query's case-insensitive comparison.
const adminPositionName = "Admin"

func main() {
	username := flag.String("username", "", "login username for the new Admin Employee (required)")
	password := flag.String("password", "", "initial password for the new Admin Employee (required)")
	fullName := flag.String("full-name", "", "full name for the new Admin Employee (required)")
	email := flag.String("email", "", "email for the new Admin Employee (required)")
	odooEmployeeID := flag.Int64("odoo-employee-id", 0, "odoo_employee_id to store on the new row (required, see package doc)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	if *username == "" || *password == "" || *fullName == "" || *email == "" || *odooEmployeeID == 0 {
		flag.Usage()
		os.Exit(2)
	}

	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("failed to load .env file", "error", err)
	}

	ctx := context.Background()
	dsn := env.GetString("DATABASE_DSN", "host=localhost user=postgres password=postgres dbname=employees sslmode=disable")

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := run(ctx, pool, *username, *password, *fullName, *email, *odooEmployeeID); err != nil {
		logger.Error("bootstrap admin failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, pool *pgxpool.Pool, username, password, fullName, email string, odooEmployeeID int64) error {
	q := repo.New(pool)

	position, err := findOrCreateAdminPosition(ctx, q)
	if err != nil {
		return fmt.Errorf("find or create %q position: %w", adminPositionName, err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	employee, err := q.CreateEmployee(ctx, repo.CreateEmployeeParams{
		OdooEmployeeID: odooEmployeeID,
		FullName:       fullName,
		Email:          email,
		Username:       username,
	})
	if err != nil {
		return fmt.Errorf("create employee: %w", err)
	}

	if _, err := q.SetEmployeePassword(ctx, repo.SetEmployeePasswordParams{
		ID:       employee.ID,
		Password: hashed,
	}); err != nil {
		return fmt.Errorf("set password: %w", err)
	}

	if err := q.InsertEmployeePositions(ctx, repo.InsertEmployeePositionsParams{
		EmployeeID:  employee.ID,
		PositionIds: []int64{position.ID},
	}); err != nil {
		return fmt.Errorf("assign %q position: %w", adminPositionName, err)
	}

	fmt.Printf("created Admin employee id=%d username=%q\n", employee.ID, employee.Username)
	return nil
}

// findOrCreateAdminPosition makes the script idempotent to run against an
// environment that already has an "Admin" Position (e.g. a second Admin
// account bootstrapped later) — matched case-insensitively, the same
// comparison auth.IsEmployeeAdmin uses (ADR-0015).
func findOrCreateAdminPosition(ctx context.Context, q repo.Querier) (repo.Position, error) {
	existing, err := q.ListPositions(ctx)
	if err != nil {
		return repo.Position{}, err
	}
	for _, p := range existing {
		if strings.EqualFold(p.Name, adminPositionName) {
			return p, nil
		}
	}
	return q.CreatePosition(ctx, adminPositionName)
}
