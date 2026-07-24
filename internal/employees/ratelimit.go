package employees

import (
	"context"
	"net/netip"
	"strings"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// Issue #39: password-reset requests are throttled per email and per IP over
// a shared rolling window, independently of each other — either limit alone
// is enough to withhold the email.
const (
	passwordResetRequestWindow     = 15 * time.Minute
	passwordResetRequestEmailLimit = 3
	passwordResetRequestIPLimit    = 10
)

// Issue #42: LockPasswordResetRequestKey's classid namespaces the email and
// IP advisory-lock spaces apart so a hashtext collision between an email
// string and an IP string can never let the two dimensions lock each other
// out. These values have no meaning beyond distinguishing the two spaces —
// they aren't shared with any other lock user in this codebase yet.
const (
	passwordResetLockClassEmail int32 = 1
	passwordResetLockClassIP    int32 = 2
)

// allowPasswordResetRequest records this attempt in password_reset_requests
// and reports whether RequestPasswordReset should actually go on to send an
// email. The row is written unconditionally — even once a caller is over
// limit — because the table tracks every incoming request, not just the
// ones that resulted in an email; only the caller's downstream mailer call
// is gated on the returned bool. Cleanup of rows older than the window runs
// first (opportunistic, no separate job) so the counts below never grow
// unbounded. Any error at any step aborts the remaining steps and returns
// false, which RequestPasswordReset must treat identically to "over limit"
// (no email, no observable difference to the caller).
//
// Issue #42: the whole cleanup/count/insert sequence runs inside one
// transaction, guarded by two Postgres advisory locks (email, then IP, in
// that fixed order — see LockPasswordResetRequestKey) taken before any of
// it. Both locks are transaction-scoped (pg_advisory_xact_lock), so they
// release automatically on commit or rollback; a concurrent request for the
// same email or IP blocks at the lock rather than reading a pre-insert
// count, closing the TOCTOU race a plain READ COMMITTED transaction alone
// wouldn't (see the issue for why READ COMMITTED isn't sufficient on its
// own). Concurrent requests for unrelated emails/IPs still proceed in
// parallel, since each pair of locks is keyed independently.
func (s *service) allowPasswordResetRequest(ctx context.Context, email string, clientIP netip.Addr) (bool, error) {
	// employees.email is CITEXT (case-insensitive); password_reset_requests.email
	// is plain TEXT, so this table must be lowercased on the way in — otherwise
	// resubmitting the same address with different casing evades the per-email
	// throttle below despite resolving to the same employee row.
	email = strings.ToLower(email)

	since := pgtype.Timestamptz{Time: time.Now().Add(-passwordResetRequestWindow), Valid: true}

	var allow bool
	err := s.withTx(ctx, func(q repo.Querier) error {
		if err := q.LockPasswordResetRequestKey(ctx, repo.LockPasswordResetRequestKeyParams{
			Classid: passwordResetLockClassEmail,
			Key:     email,
		}); err != nil {
			return err
		}

		if err := q.LockPasswordResetRequestKey(ctx, repo.LockPasswordResetRequestKeyParams{
			Classid: passwordResetLockClassIP,
			Key:     clientIP.String(),
		}); err != nil {
			return err
		}

		if _, err := q.DeletePasswordResetRequestsOlderThan(ctx, since); err != nil {
			return err
		}

		emailCount, err := q.CountPasswordResetRequestsByEmail(ctx, repo.CountPasswordResetRequestsByEmailParams{
			Email: email,
			Since: since,
		})
		if err != nil {
			return err
		}

		ipCount, err := q.CountPasswordResetRequestsByIPAddress(ctx, repo.CountPasswordResetRequestsByIPAddressParams{
			IpAddress: clientIP,
			Since:     since,
		})
		if err != nil {
			return err
		}

		if err := q.CreatePasswordResetRequest(ctx, repo.CreatePasswordResetRequestParams{
			IpAddress: clientIP,
			Email:     email,
		}); err != nil {
			return err
		}

		allow = emailCount < passwordResetRequestEmailLimit && ipCount < passwordResetRequestIPLimit
		return nil
	})
	if err != nil {
		return false, err
	}

	return allow, nil
}
