package employees

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/dbx"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/env"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/mailer"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/pgerr"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/syncx"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/tokenx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// Constraint names come from internal/adapters/postgresql/migrations/00001_create_employees.sql
// (Postgres' default naming: <table>_<column>_key); employees_odoo_employee_id_key
// was renamed in migration 00009 alongside the employee_id -> odoo_employee_id
// column rename.
const (
	employeesEmailKeyConstraint          = "employees_email_key"
	employeesOdooEmployeeIDKeyConstraint = "employees_odoo_employee_id_key"
	employeesUsernameKeyConstraint       = "employees_username_key"
)

// employeePositionsPositionIDFkeyConstraint comes from
// internal/adapters/postgresql/migrations/00011_create_employee_positions.sql
// (Postgres' default naming: <table>_<column>_fkey).
const employeePositionsPositionIDFkeyConstraint = "employee_positions_position_id_fkey"

// activationTokenTTL matches the existing password-reset scope (30 minutes),
// per feat-007's explicit choice over a longer first-activation-specific TTL.
const activationTokenTTL = 30 * time.Minute

// activationTokenBytes is issuePasswordResetToken's raw token length before
// hex encoding (see tokenx.Generate).
const activationTokenBytes = 32

// employeeSyncBatchSize is how many ids runSync sends to
// odoo.FetchEmployeesByEmployeeIDs per call — a SyncEmployees request isn't
// bounded in how many ids it accepts, so it pages through them this many at
// a time instead of sending them all in one round trip.
const employeeSyncBatchSize = 50

// employeeSyncTimeout bounds runSync's detached goroutine so a stalled Odoo
// or database call can't leave syncGuard stuck held indefinitely — the
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

	syncGuard syncx.Guard

	// passwordResetLocks serializes issuePasswordResetToken per Employee —
	// see the lock/defer unlock there for why.
	passwordResetLocks syncx.KeyedMutex[int64]
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

// validateOdooEmployeeID confirms odooEmployeeID exists in Odoo
// (existence-only — the returned FullName/Email are never used to
// overwrite what the caller submitted). Fails closed on ADR-0007's terms:
// no match, or the Odoo call itself failing, both reject with the same
// sentinel error.
func (s *service) validateOdooEmployeeID(ctx context.Context, odooEmployeeID int64) error {
	found, err := s.odoo.FetchEmployeesByOdooEmployeeIDs(ctx, []int64{odooEmployeeID})
	if err != nil {
		return ErrOdooEmployeeIDNotFound
	}
	for _, e := range found {
		if e.OdooEmployeeID == odooEmployeeID {
			return nil
		}
	}
	return ErrOdooEmployeeIDNotFound
}

func (s *service) CreateEmployee(ctx context.Context, params createEmployeeParams) (EmployeeDetail, error) {
	if err := s.validateOdooEmployeeID(ctx, params.OdooEmployeeID); err != nil {
		return EmployeeDetail{}, err
	}

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
				return translateInsertEmployeePositionsForeignKeyViolation(err)
			}
		}

		positionIDs := params.PositionIDs
		if positionIDs == nil {
			positionIDs = []int64{}
		}
		// A brand-new employee has no store membership yet — only
		// SyncEmployees ever populates employee_stores (ADR-0009).
		detail = EmployeeDetail{Employee: employee, PositionIDs: positionIDs, StoreIDs: []int64{}}
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

func (s *service) ListEmployees(ctx context.Context, filter ListEmployeesFilter) ([]EmployeeDetail, error) {
	employees, err := s.repo.ListEmployees(ctx, repo.ListEmployeesParams{
		Q:               textPtrToText(filter.Q),
		PositionIds:     filter.PositionIDs,
		StoreIds:        filter.StoreIDs,
		OdooEmployeeIds: filter.OdooEmployeeIDs,
		IsActive:        boolPtrToBool(filter.IsActive),
	})
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

	storesByEmployee, err := s.repo.ListStoreIDsByEmployeeIDs(ctx, ids)
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

func (s *service) UpdateEmployee(ctx context.Context, id int64, params updateEmployeeParams) (EmployeeDetail, error) {
	current, err := s.repo.GetEmployeeByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EmployeeDetail{}, ErrEmployeeNotFound
		}
		return EmployeeDetail{}, err
	}
	// Only re-validate when odooEmployeeId is actually changing — a routine
	// edit (name, positions, ...) that leaves it untouched must not be
	// slowed down or blocked by an unrelated Odoo outage (ADR-0007).
	if params.OdooEmployeeID != current.OdooEmployeeID {
		if err := s.validateOdooEmployeeID(ctx, params.OdooEmployeeID); err != nil {
			return EmployeeDetail{}, err
		}
	}

	// Hashed up front (bcrypt needs no DB access) so the write below — if a
	// password was submitted — can happen inside the same transaction as
	// the rest of the update, rather than as a separate call that could
	// leave the employee row updated but the password unset if it failed.
	var hashedPassword []byte
	if params.Password != nil {
		hashedPassword, err = bcrypt.GenerateFromPassword([]byte(*params.Password), bcrypt.DefaultCost)
		if err != nil {
			return EmployeeDetail{}, err
		}
	}

	positionIDs := params.PositionIDs
	if positionIDs == nil {
		positionIDs = []int64{}
	}

	var detail EmployeeDetail
	err = s.withTx(ctx, func(q repo.Querier) error {
		if err := validatePositionIDs(ctx, q, positionIDs); err != nil {
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

		if err := dbx.DiffReplace(ctx,
			func(ctx context.Context) error {
				return q.DeleteEmployeePositionsNotIn(ctx, repo.DeleteEmployeePositionsNotInParams{
					EmployeeID:  id,
					PositionIds: positionIDs,
				})
			},
			func(ctx context.Context) error {
				if err := q.InsertEmployeePositions(ctx, repo.InsertEmployeePositionsParams{
					EmployeeID:  id,
					PositionIds: positionIDs,
				}); err != nil {
					return translateInsertEmployeePositionsForeignKeyViolation(err)
				}
				return nil
			},
		); err != nil {
			return err
		}

		if hashedPassword != nil {
			if _, err := q.SetEmployeePassword(ctx, repo.SetEmployeePasswordParams{
				ID:       id,
				Password: hashedPassword,
			}); err != nil {
				return err
			}
		}

		// Store membership is Odoo-owned and untouched by this update — just
		// reflect its current state (see ADR-0009).
		storeIDs, err := q.ListStoreIDsByEmployeeID(ctx, id)
		if err != nil {
			return err
		}
		if storeIDs == nil {
			storeIDs = []int64{}
		}
		detail = EmployeeDetail{Employee: employee, PositionIDs: positionIDs, StoreIDs: storeIDs}
		return nil
	})
	if err != nil {
		return EmployeeDetail{}, err
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
// employee's password (bcrypt-hashed). Serves first-time activation, an
// admin-triggered reset, and a self-service reset (issue #38) alike — all
// three send the employee the same kind of token, and completing any of them
// is the same operation from the DB's point of view. RedeemPasswordResetToken's
// UPDATE...RETURNING is atomic: its row lock means only one concurrent caller
// can redeem a given token, so the password update below only ever runs
// after that caller has exclusively claimed it.
//
// On success (issue #38), the employee's existing Session (if any) is
// deleted and their failed-login-attempt count/lockout is cleared, so they
// can log in immediately with the new password on any device, and a stale
// lockout from before the reset doesn't block that first login. Both are
// best-effort cleanup of state that's meaningless once the password has
// already changed, so a failure here is logged rather than failing the
// activation/reset itself — the employee's password is already set at this
// point.
func (s *service) CompleteActivation(ctx context.Context, params completeActivationParams) error {
	resetToken, err := s.repo.RedeemPasswordResetToken(ctx, tokenx.Hash(params.Token))
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

	if _, err := s.repo.DeleteSessionByEmployeeID(ctx, resetToken.EmployeeID); err != nil {
		slog.Error("employees: delete session on activation/reset", "employee_id", resetToken.EmployeeID, "error", err)
	}
	if err := s.repo.ResetFailedLoginAttempts(ctx, resetToken.EmployeeID); err != nil {
		slog.Error("employees: reset failed login attempts on activation/reset", "employee_id", resetToken.EmployeeID, "error", err)
	}

	return nil
}

// RequestPasswordReset is the public, unauthenticated
// POST /password-reset-requests endpoint's entry point (issue #38). It has
// no error return: the handler always returns the same generic 200
// regardless of what happens here, so every branch below either sends an
// email or silently does nothing, and any lookup/issuance/send failure is
// swallowed (logged only) rather than surfaced — surfacing it would let a
// caller distinguish "unknown email" (fails fast, no DB error) from "known
// email, transient failure" (same generic response either way) by
// timing/behavior, which is exactly the enumeration signal this endpoint
// must not leak.
//
// Branches by Employee state at request time (see issue #36's spec):
//   - unknown email, or found but is_active = false: no email sent.
//   - found, active, no password ever set (pending activation): resend the
//     activation email instead of a reset email — mirrors
//     auth.Service.Login's notActivated check (len(Password) == 0).
//   - found, active, password already set: send the password-reset email.
func (s *service) RequestPasswordReset(ctx context.Context, email string) {
	employee, err := s.repo.GetEmployeeByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("employees: request password reset lookup", "error", err)
		}
		return
	}

	if !employee.IsActive {
		return
	}

	if len(employee.Password) == 0 {
		s.sendActivationEmail(ctx, employee)
		return
	}

	if err := s.sendPasswordResetEmail(ctx, employee); err != nil {
		slog.Error("employees: send password reset email", "employee_id", employee.ID, "error", err)
	}
}

// sendPasswordResetEmail issues a token via issuePasswordResetToken and
// emails the resulting link using the password-reset template, returning
// whichever of the two failed so each caller can apply its own error policy
// (surfaced per-id in BulkSendPasswordResetLinks vs swallowed-and-logged in
// RequestPasswordReset).
func (s *service) sendPasswordResetEmail(ctx context.Context, employee repo.Employee) error {
	link, err := s.issuePasswordResetToken(ctx, employee, "/reset-password")
	if err != nil {
		return err
	}

	data := mailer.PasswordResetData{
		FullName:   employee.FullName,
		Link:       link,
		TTLMinutes: int(activationTokenTTL.Minutes()),
	}
	return s.mailer.Send(ctx, employee.Email, mailer.PasswordResetTemplate, data)
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

	storeIDs, err := s.repo.ListStoreIDsByEmployeeID(ctx, id)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if storeIDs == nil {
		storeIDs = []int64{}
	}

	return EmployeeDetail{Employee: employee, PositionIDs: positionIDs, StoreIDs: storeIDs}, nil
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

		if err := s.sendPasswordResetEmail(ctx, employee); err != nil {
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
// ErrSyncInProgress rather than queued or run in parallel, guarded by
// syncGuard (the "2 admins click the button" special case).
func (s *service) SyncEmployees(ctx context.Context, ids []int64) error {
	if !s.syncGuard.TryStart() {
		return ErrSyncInProgress
	}

	odooEmployeeIDs, err := s.repo.ListEmployeeIDsByIDs(ctx, ids)
	if err != nil {
		s.syncGuard.Finish()
		return err
	}

	// Detached from ctx: the HTTP handler's request context is canceled the
	// moment it returns, which would race with (and likely abort) this
	// goroutine if it inherited that cancellation. Still bounded by
	// employeeSyncTimeout so a stalled Odoo/DB call can't hold the guard
	// true forever; runSync owns cancel and releases it when it returns.
	syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), employeeSyncTimeout)
	go s.runSync(syncCtx, cancel, odooEmployeeIDs)

	return nil
}

// SyncStatus reports whether a background sync started by SyncEmployees is
// still running, so the frontend can poll it to keep its trigger button
// disabled for the duration.
func (s *service) SyncStatus(ctx context.Context) SyncStatus {
	return SyncStatus{Syncing: s.syncGuard.Syncing()}
}

// runSync does the actual Odoo fetch + bulk upsert (Steps 4-5), paging
// through ids employeeSyncBatchSize at a time so a request isn't bounded by
// how many ids Odoo accepts in one call. It logs the outcome since nothing
// else observes this goroutine once SyncEmployees has already returned to
// its caller. A batch's fetch or upsert error aborts the remaining batches
// rather than skipping past them.
func (s *service) runSync(ctx context.Context, cancel context.CancelFunc, ids []int64) {
	defer cancel()
	defer s.syncGuard.Finish()

	var notFound []int64
	var skippedStoreIDs []int
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
		for i, e := range found {
			foundIDs[e.OdooEmployeeID] = true
			odooEmployeeIDs[i] = e.OdooEmployeeID
			fullNames[i] = e.FullName
			emails[i] = e.Email
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
		})
		if err != nil {
			slog.Error("employees: sync upsert", "ids", odooEmployeeIDs, "error", err)
			return
		}

		internalIDs := make(map[int64]int64, len(rows))
		for _, row := range rows {
			internalIDs[row.OdooEmployeeID] = row.ID
			if row.Inserted {
				inserted++
			} else {
				updated++
			}
		}

		skippedStoreIDs = append(skippedStoreIDs, s.syncEmployeeStores(ctx, found, internalIDs)...)
	}

	if len(notFound) > 0 {
		slog.Warn("employees: sync ids not found in odoo", "ids", notFound)
	}
	if len(skippedStoreIDs) > 0 {
		slog.Warn("employees: sync store ids not resolvable to a local store", "odoo_store_ids", skippedStoreIDs)
	}
	slog.Info("employees: sync completed", "inserted", inserted, "updated", updated, "not_found", len(notFound))
}

// syncEmployeeStores resolves each employee's Odoo store ids (x_pos_shop_ids,
// ADR-0009) to this system's internal store.id via store.odoo_store_id —
// the same join key SyncStores already uses — then diffs employee_stores
// per employee: insert newly resolved, delete no-longer-present, leave
// unchanged. internalIDs maps this batch's odoo_employee_id values to their
// internal employee id (from the UpsertEmployees call that just ran).
// Returns the Odoo store ids that didn't resolve to a known local store,
// for the caller to log — one unresolvable store id is skipped for that one
// assignment, never failing the rest of that employee's sync or any other
// employee's (see ADR-0009).
func (s *service) syncEmployeeStores(ctx context.Context, employees []odoo.Employee, internalIDs map[int64]int64) []int {
	odooStoreIDSet := make(map[int]bool)
	for _, e := range employees {
		for _, id := range e.StoreIDs {
			odooStoreIDSet[id] = true
		}
	}

	odooStoreIDs := make([]int, 0, len(odooStoreIDSet))
	for id := range odooStoreIDSet {
		odooStoreIDs = append(odooStoreIDs, id)
	}
	sort.Ints(odooStoreIDs) // deterministic query order — map iteration isn't

	// resolved stays empty (rather than short-circuiting here) when no
	// employee in this batch reports any Odoo store, so the loop below
	// still runs DeleteEmployeeStoresNotIn with an empty set per employee —
	// clearing stale employee_stores rows for staff Odoo has since removed
	// from every store, instead of leaving them dangling.
	resolved := make(map[int]int64, len(odooStoreIDs))
	if len(odooStoreIDs) > 0 {
		odooStoreIDStrs := make([]string, len(odooStoreIDs))
		for i, id := range odooStoreIDs {
			odooStoreIDStrs[i] = strconv.Itoa(id)
		}

		stores, err := s.repo.ListStoresByOdooStoreIDs(ctx, odooStoreIDStrs)
		if err != nil {
			slog.Error("employees: sync resolve store ids", "error", err)
			return nil
		}

		for _, st := range stores {
			odooID, err := strconv.Atoi(st.OdooStoreID.String)
			if err != nil {
				continue
			}
			resolved[odooID] = st.ID
		}
	}

	var skipped []int
	for _, e := range employees {
		internalID, ok := internalIDs[e.OdooEmployeeID]
		if !ok {
			continue
		}

		storeIDs := make([]int64, 0, len(e.StoreIDs))
		for _, odooStoreID := range e.StoreIDs {
			storeID, ok := resolved[odooStoreID]
			if !ok {
				skipped = append(skipped, odooStoreID)
				continue
			}
			storeIDs = append(storeIDs, storeID)
		}

		// Delete and insert run inside one transaction per employee, so a
		// failing insert rolls back the delete too rather than leaving this
		// employee with neither their old nor new store memberships. One
		// employee's failure is logged and skipped — it doesn't abort the
		// rest of the batch.
		err := s.withTx(ctx, func(q repo.Querier) error {
			return dbx.DiffReplace(ctx,
				func(ctx context.Context) error {
					return q.DeleteEmployeeStoresNotIn(ctx, repo.DeleteEmployeeStoresNotInParams{
						EmployeeID: internalID,
						StoreIds:   storeIDs,
					})
				},
				func(ctx context.Context) error {
					return q.InsertEmployeeStores(ctx, repo.InsertEmployeeStoresParams{
						EmployeeID: internalID,
						StoreIds:   storeIDs,
					})
				},
			)
		})
		if err != nil {
			slog.Error("employees: sync store membership", "employee_id", internalID, "error", err)
			continue
		}
	}

	return skipped
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
	token, err := tokenx.Generate(activationTokenBytes)
	if err != nil {
		return "", err
	}

	// Concurrent issuance for the same Employee (e.g. two admins clicking
	// "resend" at once) must serialize around invalidate-then-insert:
	// without this lock, both requests could invalidate the prior token
	// before either inserts its new one, leaving two redeemable tokens.
	// Different Employees never contend for the same lock.
	unlock := s.passwordResetLocks.Lock(employee.ID)
	defer unlock()

	// Invalidating prior tokens and inserting the new one run in the same
	// transaction so a failure between the two never leaves the Employee
	// with zero redeemable tokens.
	err = s.withTx(ctx, func(q repo.Querier) error {
		if err := q.InvalidatePasswordResetTokensByEmployeeID(ctx, employee.ID); err != nil {
			return err
		}
		_, err := q.CreatePasswordResetToken(ctx, repo.CreatePasswordResetTokenParams{
			EmployeeID: employee.ID,
			TokenHash:  tokenx.Hash(token),
			ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(activationTokenTTL), Valid: true},
		})
		return err
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s%s?token=%s", env.GetString("APP_URL", "http://localhost:3000"), linkPath, token), nil
}

// textPtrToText converts an optional filter field to the nullable
// pgtype.Text ListEmployees' query expects — nil means "don't filter on
// this facet" (mirrors internal/stores/service.go's float64PtrToNumeric).
func textPtrToText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// boolPtrToBool converts an optional filter field to the nullable
// pgtype.Bool ListEmployees' query expects — see textPtrToText.
func boolPtrToBool(b *bool) pgtype.Bool {
	if b == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *b, Valid: true}
}

// translateEmployeeUniqueViolation maps known Postgres unique-violation
// errors to the package's sentinel conflict errors, leaving every other
// error untouched. Shared by CreateEmployee and UpdateEmployee.
func translateEmployeeUniqueViolation(err error) error {
	return pgerr.Translate(err, pgerr.UniqueViolation, map[string]error{
		employeesEmailKeyConstraint:          ErrEmailAlreadyExists,
		employeesOdooEmployeeIDKeyConstraint: ErrOdooEmployeeIDAlreadyExists,
		employeesUsernameKeyConstraint:       ErrUsernameAlreadyExists,
	})
}

// translateInsertEmployeePositionsForeignKeyViolation maps a Postgres
// foreign-key violation on employee_positions.position_id to
// ErrUnknownPositionID, out of the narrow race window between
// validatePositionIDs' pre-check and this insert, leaving every other error
// untouched. employee_id can't violate here — the employee row was just
// written earlier in the same transaction.
func translateInsertEmployeePositionsForeignKeyViolation(err error) error {
	return pgerr.Translate(err, pgerr.ForeignKeyViolation, map[string]error{
		employeePositionsPositionIDFkeyConstraint: ErrUnknownPositionID,
	})
}
