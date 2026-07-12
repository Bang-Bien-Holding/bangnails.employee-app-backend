package employees

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/env"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/mailer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/bcrypt"
)

// uniqueViolationCode is Postgres' SQLSTATE for a unique_violation error.
const uniqueViolationCode = "23505"

// Constraint names come from internal/adapters/postgresql/migrations/00001_create_employees.sql
// (Postgres' default naming: <table>_<column>_key).
const (
	employeesEmailKeyConstraint      = "employees_email_key"
	employeesEmployeeIDKeyConstraint = "employees_employee_id_key"
	employeesUsernameKeyConstraint   = "employees_username_key"
)

// activationTokenTTL matches the existing password-reset scope (30 minutes),
// per feat-007's explicit choice over a longer first-activation-specific TTL.
const activationTokenTTL = 30 * time.Minute

type service struct {
	repo   repo.Querier
	mailer mailer.Client
}

func NewService(r repo.Querier, m mailer.Client) Service {
	return &service{repo: r, mailer: m}
}

func (s *service) CreateEmployee(ctx context.Context, params createEmployeeParams) (repo.Employee, error) {
	employee, err := s.repo.CreateEmployee(ctx, repo.CreateEmployeeParams{
		EmployeeID: params.EmployeeID,
		FullName:   params.FullName,
		Email:      params.Email,
		Username:   params.Username,
		Role:       params.Role,
	})
	if err != nil {
		return repo.Employee{}, translateEmployeeUniqueViolation(err)
	}

	// Detached from ctx: the HTTP handler's request context is canceled the
	// moment it returns, which would race with (and likely abort) this
	// goroutine if it inherited that cancellation.
	go s.sendActivationEmail(context.WithoutCancel(ctx), employee)

	return employee, nil
}

func (s *service) ListEmployees(ctx context.Context) ([]repo.Employee, error) {
	return s.repo.ListEmployees(ctx)
}

func (s *service) UpdateEmployee(ctx context.Context, id int64, params updateEmployeeParams) (repo.Employee, error) {
	employee, err := s.repo.UpdateEmployee(ctx, repo.UpdateEmployeeParams{
		ID:         id,
		EmployeeID: params.EmployeeID,
		FullName:   params.FullName,
		Email:      params.Email,
		Username:   params.Username,
		Role:       params.Role,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repo.Employee{}, ErrEmployeeNotFound
		}
		return repo.Employee{}, translateEmployeeUniqueViolation(err)
	}

	if params.Password != nil {
		if err := s.SetEmployeePassword(ctx, id, *params.Password); err != nil {
			return repo.Employee{}, err
		}
	}

	return employee, nil
}

// SetEmployeePassword lets an admin directly assign an employee's password,
// bypassing the token/email flow used by CompleteActivation.
func (s *service) SetEmployeePassword(ctx context.Context, id int64, password string) error {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	rowsAffected, err := s.repo.SetEmployeePassword(ctx, repo.SetEmployeePasswordParams{
		ID:       id,
		Password: hashed,
	})
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrEmployeeNotFound
	}
	return nil
}

func (s *service) SetEmployeeActive(ctx context.Context, id int64, isActive bool) error {
	rowsAffected, err := s.repo.SetEmployeeActive(ctx, repo.SetEmployeeActiveParams{
		ID:       id,
		IsActive: isActive,
	})
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrEmployeeNotFound
	}
	return nil
}

func (s *service) DeleteEmployee(ctx context.Context, id int64) error {
	rowsAffected, err := s.repo.DeleteEmployee(ctx, id)
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrEmployeeNotFound
	}
	return nil
}

// BulkDeleteEmployees deletes each id independently and best-effort: one
// unknown or failing id is reported in its own result rather than blocking
// or rolling back the rest of the batch (user's explicit choice).
func (s *service) BulkDeleteEmployees(ctx context.Context, ids []int64) []BulkActionResult {
	results := make([]BulkActionResult, len(ids))
	for i, id := range ids {
		err := s.DeleteEmployee(ctx, id)
		results[i] = BulkActionResult{ID: id, Success: err == nil}
		if err != nil {
			results[i].Error = err.Error()
		}
	}
	return results
}

// CompleteActivation validates the token (unexpired, unused), sets the
// employee's password (bcrypt-hashed), and marks the token used. Serves
// both first-time activation and an admin-triggered reset — both send the
// employee the same kind of token, and completing either is the same
// operation from the DB's point of view. Not wrapped in a DB transaction:
// SetEmployeePassword and MarkPasswordResetTokenUsed are two separate
// calls, same as the rest of this service today (see the deferred
// CreateEmployee+CreatePasswordResetToken transaction note from feat-007).
func (s *service) CompleteActivation(ctx context.Context, params completeActivationParams) error {
	resetToken, err := s.repo.GetValidPasswordResetToken(ctx, params.Token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidOrExpiredToken
		}
		return err
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(params.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	if _, err := s.repo.SetEmployeePassword(ctx, repo.SetEmployeePasswordParams{
		ID:       resetToken.EmployeeID,
		Password: hashed,
	}); err != nil {
		return err
	}

	return s.repo.MarkPasswordResetTokenUsed(ctx, resetToken.ID)
}

func (s *service) GetEmployeeByID(ctx context.Context, id int64) (repo.Employee, error) {
	employee, err := s.repo.GetEmployeeByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return repo.Employee{}, ErrEmployeeNotFound
		}
		return repo.Employee{}, err
	}
	return employee, nil
}

// BulkSendPasswordResetLinks sends a password-set/reset link to each id,
// independently and best-effort (user's explicit choice, same as
// BulkDeleteEmployees): one unknown id, deactivated employee, or mailer
// failure is reported in its own result rather than blocking the batch.
// Unlike sendActivationEmail (fired async on employee creation, failures
// only logged), this runs synchronously per id and surfaces every failure
// in the result — an admin explicitly triggering this wants to know
// whether each email actually went out.
func (s *service) BulkSendPasswordResetLinks(ctx context.Context, ids []int64) []BulkActionResult {
	results := make([]BulkActionResult, len(ids))
	for i, id := range ids {
		results[i] = BulkActionResult{ID: id}

		employee, err := s.GetEmployeeByID(ctx, id)
		if err != nil {
			results[i].Error = err.Error()
			continue
		}
		if !employee.IsActive {
			results[i].Error = ErrEmployeeNotActive.Error()
			continue
		}

		link, err := s.issuePasswordResetToken(ctx, employee, "/reset-password")
		if err != nil {
			results[i].Error = err.Error()
			continue
		}

		data := mailer.PasswordResetData{
			FullName:   employee.FullName,
			Link:       link,
			TTLMinutes: int(activationTokenTTL.Minutes()),
		}
		if err := s.mailer.Send(ctx, employee.Email, mailer.PasswordResetTemplate, data); err != nil {
			results[i].Error = err.Error()
			continue
		}

		results[i].Success = true
	}
	return results
}

// sendActivationEmail generates a password-reset/activation token and emails
// the employee an activation link. It runs in the background (see the `go`
// call above) so CreateEmployee doesn't block the caller on mailer latency.
// The employee row is already committed at this point, so any failure here
// (token generation, DB error, mailer error) is logged and swallowed rather
// than failing CreateEmployee — an admin should not see employee creation
// fail just because the follow-up email didn't go out.
func (s *service) sendActivationEmail(ctx context.Context, employee repo.Employee) {
	link, err := s.issuePasswordResetToken(ctx, employee, "/activate")
	if err != nil {
		slog.Error("employees: issue activation token", "employee_id", employee.ID, "error", err)
		return
	}

	data := mailer.AccountActivationData{
		FullName:   employee.FullName,
		Link:       link,
		TTLMinutes: int(activationTokenTTL.Minutes()),
	}

	if err := s.mailer.Send(ctx, employee.Email, mailer.AccountActivationTemplate, data); err != nil {
		slog.Error("employees: send activation email", "employee_id", employee.ID, "error", err)
	}
}

// issuePasswordResetToken generates a random token, persists it via
// CreatePasswordResetToken with the shared activationTokenTTL, and returns
// the link an employee follows to set/reset their password. linkPath
// distinguishes first-activation ("/activate") from an admin-triggered
// reset ("/reset-password") on the frontend, though both consume the same
// password_reset_tokens row via feat-008's completion endpoint.
func (s *service) issuePasswordResetToken(ctx context.Context, employee repo.Employee, linkPath string) (string, error) {
	token, err := generateActivationToken()
	if err != nil {
		return "", err
	}

	_, err = s.repo.CreatePasswordResetToken(ctx, repo.CreatePasswordResetTokenParams{
		EmployeeID: employee.ID,
		Token:      token,
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(activationTokenTTL), Valid: true},
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s%s?token=%s", env.GetString("APP_URL", "http://localhost:3000"), linkPath, token), nil
}

// generateActivationToken returns a random hex-encoded token for the
// password_reset_tokens table.
func generateActivationToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// translateEmployeeUniqueViolation maps known Postgres unique-violation
// errors to the package's sentinel conflict errors, leaving every other
// error untouched. Shared by CreateEmployee and UpdateEmployee.
func translateEmployeeUniqueViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolationCode {
		return err
	}

	switch pgErr.ConstraintName {
	case employeesEmailKeyConstraint:
		return ErrEmailAlreadyExists
	case employeesEmployeeIDKeyConstraint:
		return ErrEmployeeIDAlreadyExists
	case employeesUsernameKeyConstraint:
		return ErrUsernameAlreadyExists
	default:
		return err
	}
}
