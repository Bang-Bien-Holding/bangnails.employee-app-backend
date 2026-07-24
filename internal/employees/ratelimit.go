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
// Known limitation (issue #42): cleanup/count/insert are separate
// round trips with no transaction or row lock, so a true concurrent burst
// for the same email/IP can let more than the limit through before any
// request's insert commits. Deferred — see the issue for why.
func (s *service) allowPasswordResetRequest(ctx context.Context, email string, clientIP netip.Addr) (bool, error) {
	// employees.email is CITEXT (case-insensitive); password_reset_requests.email
	// is plain TEXT, so this table must be lowercased on the way in — otherwise
	// resubmitting the same address with different casing evades the per-email
	// throttle below despite resolving to the same employee row.
	email = strings.ToLower(email)

	since := pgtype.Timestamptz{Time: time.Now().Add(-passwordResetRequestWindow), Valid: true}

	if _, err := s.repo.DeletePasswordResetRequestsOlderThan(ctx, since); err != nil {
		return false, err
	}

	emailCount, err := s.repo.CountPasswordResetRequestsByEmail(ctx, repo.CountPasswordResetRequestsByEmailParams{
		Email: email,
		Since: since,
	})
	if err != nil {
		return false, err
	}

	ipCount, err := s.repo.CountPasswordResetRequestsByIPAddress(ctx, repo.CountPasswordResetRequestsByIPAddressParams{
		IpAddress: clientIP,
		Since:     since,
	})
	if err != nil {
		return false, err
	}

	if err := s.repo.CreatePasswordResetRequest(ctx, repo.CreatePasswordResetRequestParams{
		IpAddress: clientIP,
		Email:     email,
	}); err != nil {
		return false, err
	}

	return emailCount < passwordResetRequestEmailLimit && ipCount < passwordResetRequestIPLimit, nil
}
