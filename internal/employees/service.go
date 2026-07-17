package employees

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/env"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/mailer"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// uniqueViolationCode is Postgres' SQLSTATE for a unique_violation error.
const uniqueViolationCode = "23505"

// Constraint names come from internal/adapters/postgresql/migrations/00001_create_employees.sql
// (Postgres' default naming: <table>_<column>_key); employees_odoo_employee_id_key
// was renamed in migration 00009 alongside the employee_id -> odoo_employee_id
// column rename.
const (
	employeesEmailKeyConstraint          = "employees_email_key"
	employeesOdooEmployeeIDKeyConstraint = "employees_odoo_employee_id_key"
	employeesUsernameKeyConstraint       = "employees_username_key"
)

// activationTokenTTL matches the existing password-reset scope (30 minutes),
// per feat-007's explicit choice over a longer first-activation-specific TTL.
const activationTokenTTL = 30 * time.Minute

// employeeSyncBatchSize is how many ids runSync sends to
// odoo.FetchEmployeesByEmployeeIDs per call — a SyncEmployees request isn't
// bounded in how many ids it accepts, so it pages through them this many at
// a time instead of sending them all in one round trip.
const employeeSyncBatchSize = 50

// employeeSyncTimeout bounds runSync's detached goroutine so a stalled Odoo
// or database call can't leave s.syncing stuck true indefinitely — the
// goroutine deliberately outlives the triggering request's context, but
// still needs its own deadline.
const employeeSyncTimeout = 5 * time.Minute

type service struct {
	// repo is a plain, non-transactional Querier for reads that don't need
	// transaction scoping — GetEmployeeByID uses this rather than withTx.
	repo repo.Querier
	// withTx wraps fn in a transaction-scoped repo.Querier — a real
	// pool-backed implementation is installed by NewService; tests replace
	// it with a stub that calls fn against a mocked Querier directly.
	withTx func(ctx context.Context, fn func(repo.Querier) error) error
	mailer mailer.Client
	odoo   odoo.Client

	mu      sync.Mutex
	syncing bool
}

func NewService(pool *pgxpool.Pool, m mailer.Client, o odoo.Client) Service {
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
		mailer: m,
		odoo:   o,
	}
}

// validatePositionIDs rejects a submitted position-id set containing an id
// that isn't a real position, via one round trip comparing CountPositionsByIDs
// against the distinct submitted count (see ADR-0008). An empty/nil ids is
// always valid (an employee with no positions), so it short-circuits before
// the query.
func validatePositionIDs(ctx context.Context, q repo.Querier, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	count, err := q.CountPositionsByIDs(ctx, ids)
	if err != nil {
		return err
	}
	if count != int64(len(ids)) {
		return ErrUnknownPositionID
	}
	return nil
}

func (s *service) CreateEmployee(ctx context.Context, params createEmployeeParams) (EmployeeDetail, error) {
	var detail EmployeeDetail
	err := s.withTx(ctx, func(q repo.Querier) error {
		if err := validatePositionIDs(ctx, q, params.PositionIDs); err != nil {
			return err
		}

		employee, err := q.CreateEmployee(ctx, repo.CreateEmployeeParams{
			OdooEmployeeID: params.OdooEmployeeID,
			FullName:       params.FullName,
			Email:          params.Email,
			Username:       params.Username,
		})
		if err != nil {
			return translateEmployeeUniqueViolation(err)
		}

		if len(params.PositionIDs) > 0 {
			if err := q.InsertEmployeePositions(ctx, repo.InsertEmployeePositionsParams{
				EmployeeID:  employee.ID,
				PositionIds: params.PositionIDs,
			}); err != nil {
				return err
			}
		}

		positionIDs := params.PositionIDs
		if positionIDs == nil {
			positionIDs = []int64{}
		}
		detail = EmployeeDetail{Employee: employee, PositionIDs: positionIDs}
		return nil
	})
	if err != nil {
		return EmployeeDetail{}, err
	}

	// Detached from ctx: the HTTP handler's request context is canceled the
	// moment it returns, which would race with (and likely abort) this
	// goroutine if it inherited that cancellation.
	go s.sendActivationEmail(context.WithoutCancel(ctx), detail.Employee)

	return detail, nil
}

func (s *service) ListEmployees(ctx context.Context) ([]EmployeeDetail, error) {
	employees, err := s.repo.ListEmployees(ctx)
	if err != nil {
		return nil, err
	}

	ids := make([]int64, len(employees))
	for i, e := range employees {
		ids[i] = e.ID
	}
	positionsByEmployee, err := s.repo.ListPositionIDsByEmployeeIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	positionIDs := make(map[int64][]int64, len(employees))
	for _, p := range positionsByEmployee {
		positionIDs[p.EmployeeID] = append(positionIDs[p.EmployeeID], p.PositionID)
	}

	details := make([]EmployeeDetail, len(employees))
	for i, e := range employees {
		ids := positionIDs[e.ID]
		if ids == nil {
			ids = []int64{}
		}
		details[i] = EmployeeDetail{Employee: e, PositionIDs: ids}
	}
	return details, nil
}

func (s *service) UpdateEmployee(ctx context.Context, id int64, params updateEmployeeParams) (EmployeeDetail, error) {
	var detail EmployeeDetail
	err := s.withTx(ctx, func(q repo.Querier) error {
		if err := validatePositionIDs(ctx, q, params.PositionIDs); err != nil {
			return err
		}

		employee, err := q.UpdateEmployee(ctx, repo.UpdateEmployeeParams{
			ID:             id,
			OdooEmployeeID: params.OdooEmployeeID,
			FullName:       params.FullName,
			Email:          params.Email,
			Username:       params.Username,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrEmployeeNotFound
			}
			return translateEmployeeUniqueViolation(err)
		}

		if err := q.DeleteEmployeePositionsNotIn(ctx, repo.DeleteEmployeePositionsNotInParams{
			EmployeeID:  id,
			PositionIds: params.PositionIDs,
		}); err != nil {
			return err
		}
		if len(params.PositionIDs) > 0 {
			if err := q.InsertEmployeePositions(ctx, repo.InsertEmployeePositionsParams{
				EmployeeID:  id,
				PositionIds: params.PositionIDs,
			}); err != nil {
				return err
			}
		}

		positionIDs := params.PositionIDs
		if positionIDs == nil {
			positionIDs = []int64{}
		}
		detail = EmployeeDetail{Employee: employee, PositionIDs: positionIDs}
		return nil
	})
	if err != nil {
		return EmployeeDetail{}, err
	}

	if params.Password != nil {
		if err := s.SetEmployeePassword(ctx, id, *params.Password); err != nil {
			return EmployeeDetail{}, err
		}
	}

	return detail, nil
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

// CompleteActivation redeems the token (unexpired, unused) and sets the
// employee's password (bcrypt-hashed). Serves both first-time activation and
// an admin-triggered reset — both send the employee the same kind of token,
// and completing either is the same operation from the DB's point of view.
// RedeemPasswordResetToken's UPDATE...RETURNING is atomic: its row lock
// means only one concurrent caller can redeem a given token, so the password
// update below only ever runs after that caller has exclusively claimed it.
func (s *service) CompleteActivation(ctx context.Context, params completeActivationParams) error {
	resetToken, err := s.repo.RedeemPasswordResetToken(ctx, hashToken(params.Token))
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

	_, err = s.repo.SetEmployeePassword(ctx, repo.SetEmployeePasswordParams{
		ID:       resetToken.EmployeeID,
		Password: hashed,
	})
	return err
}

func (s *service) GetEmployeeByID(ctx context.Context, id int64) (EmployeeDetail, error) {
	employee, err := s.repo.GetEmployeeByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EmployeeDetail{}, ErrEmployeeNotFound
		}
		return EmployeeDetail{}, err
	}

	positionIDs, err := s.repo.ListPositionIDsByEmployeeID(ctx, id)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if positionIDs == nil {
		positionIDs = []int64{}
	}

	return EmployeeDetail{Employee: employee, PositionIDs: positionIDs}, nil
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

		detail, err := s.GetEmployeeByID(ctx, id)
		if err != nil {
			results[i].Error = err.Error()
			continue
		}
		employee := detail.Employee
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

// SyncEmployees looks up the Odoo odoo_employee_id for each given internal
// id, then pulls those from Odoo and bulk-upserts them into employees, in a
// detached goroutine so the caller gets a quick response (Step 3 of the sync
// spec) instead of waiting on Odoo/DB latency. An id with no matching row is
// silently dropped by ListEmployeeIDsByIDs rather than failing the request.
// Only one sync runs at a time — a concurrent call is rejected with
// ErrSyncInProgress rather than queued or run in parallel, guarded by mu
// (the "2 admins click the button" special case).
func (s *service) SyncEmployees(ctx context.Context, ids []int64) error {
	if !s.tryLock() {
		return ErrSyncInProgress
	}

	odooEmployeeIDs, err := s.repo.ListEmployeeIDsByIDs(ctx, ids)
	if err != nil {
		s.unlock()
		return err
	}

	// Detached from ctx: the HTTP handler's request context is canceled the
	// moment it returns, which would race with (and likely abort) this
	// goroutine if it inherited that cancellation. Still bounded by
	// employeeSyncTimeout so a stalled Odoo/DB call can't hold s.syncing
	// true forever; runSync owns cancel and releases it when it returns.
	syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), employeeSyncTimeout)
	go s.runSync(syncCtx, cancel, odooEmployeeIDs)

	return nil
}

// SyncStatus reports whether a background sync started by SyncEmployees is
// still running, so the frontend can poll it to keep its trigger button
// disabled for the duration.
func (s *service) SyncStatus(ctx context.Context) SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SyncStatus{Syncing: s.syncing}
}

// runSync does the actual Odoo fetch + bulk upsert (Steps 4-5), paging
// through ids employeeSyncBatchSize at a time so a request isn't bounded by
// how many ids Odoo accepts in one call. It logs the outcome since nothing
// else observes this goroutine once SyncEmployees has already returned to
// its caller. A batch's fetch or upsert error aborts the remaining batches
// rather than skipping past them.
func (s *service) runSync(ctx context.Context, cancel context.CancelFunc, ids []int64) {
	defer cancel()
	defer s.unlock()

	var notFound []int64
	var inserted, updated int

	for start := 0; start < len(ids); start += employeeSyncBatchSize {
		end := min(start+employeeSyncBatchSize, len(ids))
		batch := ids[start:end]

		found, err := s.odoo.FetchEmployeesByOdooEmployeeIDs(ctx, batch)
		if err != nil {
			slog.Error("employees: sync fetch from odoo", "batch_size", len(batch), "error", err)
			return
		}

		foundIDs := make(map[int64]bool, len(found))
		odooEmployeeIDs := make([]int64, len(found))
		fullNames := make([]string, len(found))
		emails := make([]string, len(found))
		usernames := make([]string, len(found))
		for i, e := range found {
			foundIDs[e.OdooEmployeeID] = true
			odooEmployeeIDs[i] = e.OdooEmployeeID
			fullNames[i] = e.FullName
			emails[i] = e.Email
			usernames[i] = e.Username
		}

		for _, id := range batch {
			if !foundIDs[id] {
				notFound = append(notFound, id)
			}
		}

		if len(found) == 0 {
			continue
		}

		rows, err := s.repo.UpsertEmployees(ctx, repo.UpsertEmployeesParams{
			OdooEmployeeIds: odooEmployeeIDs,
			FullNames:       fullNames,
			Emails:          emails,
			Usernames:       usernames,
		})
		if err != nil {
			slog.Error("employees: sync upsert", "ids", odooEmployeeIDs, "error", err)
			return
		}

		for _, row := range rows {
			if row.Inserted {
				inserted++
			} else {
				updated++
			}
		}
	}

	if len(notFound) > 0 {
		slog.Warn("employees: sync ids not found in odoo", "ids", notFound)
	}
	slog.Info("employees: sync completed", "inserted", inserted, "updated", updated, "not_found", len(notFound))
}

func (s *service) tryLock() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.syncing {
		return false
	}
	s.syncing = true
	return true
}

func (s *service) unlock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncing = false
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

// issuePasswordResetToken generates a random token, persists only its
// SHA-256 digest via CreatePasswordResetToken with the shared
// activationTokenTTL, and returns the link an employee follows to
// set/reset their password. The raw token never touches the database — it
// only ever leaves this function inside the emailed link — so a leak of the
// password_reset_tokens table (backup, replica, etc.) can't be used to
// redeem anyone's token. linkPath distinguishes first-activation
// ("/activate") from an admin-triggered reset ("/reset-password") on the
// frontend, though both consume the same password_reset_tokens row via
// feat-008's completion endpoint.
func (s *service) issuePasswordResetToken(ctx context.Context, employee repo.Employee, linkPath string) (string, error) {
	token, err := generateActivationToken()
	if err != nil {
		return "", err
	}

	_, err = s.repo.CreatePasswordResetToken(ctx, repo.CreatePasswordResetTokenParams{
		EmployeeID: employee.ID,
		TokenHash:  hashToken(token),
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

// hashToken returns the hex-encoded SHA-256 digest of a bearer token, as
// stored in and looked up from password_reset_tokens.token_hash.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
	case employeesOdooEmployeeIDKeyConstraint:
		return ErrOdooEmployeeIDAlreadyExists
	case employeesUsernameKeyConstraint:
		return ErrUsernameAlreadyExists
	default:
		return err
	}
}
