package employees

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc/mocks"
	mailermocks "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/mailer/mocks"
	odoomocks "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo/mocks"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"
)

// TestEmployeeService_AllowPasswordResetRequest covers issue #39's rate
// limiter: cleanup always runs first, the write is unconditional, and the
// allow/deny decision is gated independently on both the email and IP
// dimensions with a limit-minus-one/at-limit boundary on each. It also
// covers issue #42: the two advisory locks are acquired (email, then IP)
// before cleanup/count/insert, and an error from either lock call aborts
// the rest exactly like an error from any other step.
func TestEmployeeService_AllowPasswordResetRequest(t *testing.T) {
	ctx := context.Background()
	clientIP := netip.MustParseAddr("203.0.113.7")
	email := "van-a@example.com"
	dbErr := errors.New("connection refused")

	expectLocks := func(mockRepo *mocks.MockQuerier) {
		mockRepo.EXPECT().LockPasswordResetRequestKey(gomock.Any(), repo.LockPasswordResetRequestKeyParams{
			Classid: passwordResetLockClassEmail,
			Key:     email,
		}).Return(nil)
		mockRepo.EXPECT().LockPasswordResetRequestKey(gomock.Any(), repo.LockPasswordResetRequestKeyParams{
			Classid: passwordResetLockClassIP,
			Key:     clientIP.String(),
		}).Return(nil)
	}

	tests := []struct {
		name        string
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectAllow bool
		expectErr   error
	}{
		{
			name: "TC-RATELIMIT-01: below both limits allows and still writes the row",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				expectLocks(mockRepo)
				mockRepo.EXPECT().DeletePasswordResetRequestsOlderThan(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByEmail(gomock.Any(), gomock.Any()).Return(int64(passwordResetRequestEmailLimit-1), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByIPAddress(gomock.Any(), gomock.Any()).Return(int64(passwordResetRequestIPLimit-1), nil)
				mockRepo.EXPECT().CreatePasswordResetRequest(gomock.Any(), repo.CreatePasswordResetRequestParams{IpAddress: clientIP, Email: email}).Return(nil)
			},
			expectAllow: true,
		},
		{
			name: "TC-RATELIMIT-02: email count at limit blocks but still writes the row",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				expectLocks(mockRepo)
				mockRepo.EXPECT().DeletePasswordResetRequestsOlderThan(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByEmail(gomock.Any(), gomock.Any()).Return(int64(passwordResetRequestEmailLimit), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByIPAddress(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CreatePasswordResetRequest(gomock.Any(), gomock.Any()).Return(nil)
			},
			expectAllow: false,
		},
		{
			name: "TC-RATELIMIT-03: IP count at limit blocks but still writes the row",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				expectLocks(mockRepo)
				mockRepo.EXPECT().DeletePasswordResetRequestsOlderThan(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByEmail(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByIPAddress(gomock.Any(), gomock.Any()).Return(int64(passwordResetRequestIPLimit), nil)
				mockRepo.EXPECT().CreatePasswordResetRequest(gomock.Any(), gomock.Any()).Return(nil)
			},
			expectAllow: false,
		},
		{
			name: "TC-RATELIMIT-04: email lock error propagates and skips everything else",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().LockPasswordResetRequestKey(gomock.Any(), repo.LockPasswordResetRequestKeyParams{
					Classid: passwordResetLockClassEmail,
					Key:     email,
				}).Return(dbErr)
			},
			expectAllow: false,
			expectErr:   dbErr,
		},
		{
			name: "TC-RATELIMIT-05: IP lock error propagates and skips cleanup",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().LockPasswordResetRequestKey(gomock.Any(), repo.LockPasswordResetRequestKeyParams{
					Classid: passwordResetLockClassEmail,
					Key:     email,
				}).Return(nil)
				mockRepo.EXPECT().LockPasswordResetRequestKey(gomock.Any(), repo.LockPasswordResetRequestKeyParams{
					Classid: passwordResetLockClassIP,
					Key:     clientIP.String(),
				}).Return(dbErr)
			},
			expectAllow: false,
			expectErr:   dbErr,
		},
		{
			name: "TC-RATELIMIT-06: cleanup error propagates and skips everything else",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				expectLocks(mockRepo)
				mockRepo.EXPECT().DeletePasswordResetRequestsOlderThan(gomock.Any(), gomock.Any()).Return(int64(0), dbErr)
			},
			expectAllow: false,
			expectErr:   dbErr,
		},
		{
			name: "TC-RATELIMIT-07: email count error propagates and skips the rest",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				expectLocks(mockRepo)
				mockRepo.EXPECT().DeletePasswordResetRequestsOlderThan(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByEmail(gomock.Any(), gomock.Any()).Return(int64(0), dbErr)
			},
			expectAllow: false,
			expectErr:   dbErr,
		},
		{
			name: "TC-RATELIMIT-08: IP count error propagates and skips the insert",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				expectLocks(mockRepo)
				mockRepo.EXPECT().DeletePasswordResetRequestsOlderThan(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByEmail(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByIPAddress(gomock.Any(), gomock.Any()).Return(int64(0), dbErr)
			},
			expectAllow: false,
			expectErr:   dbErr,
		},
		{
			name: "TC-RATELIMIT-09: insert error propagates even though the caller was allowed",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				expectLocks(mockRepo)
				mockRepo.EXPECT().DeletePasswordResetRequestsOlderThan(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByEmail(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CountPasswordResetRequestsByIPAddress(gomock.Any(), gomock.Any()).Return(int64(0), nil)
				mockRepo.EXPECT().CreatePasswordResetRequest(gomock.Any(), gomock.Any()).Return(dbErr)
			},
			expectAllow: false,
			expectErr:   dbErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			mockMailer := mailermocks.NewMockClient(ctrl)
			mockOdoo := odoomocks.NewMockClient(ctrl)

			tc.setupMock(mockRepo)

			svc := newTestService(mockRepo, mockMailer, mockOdoo)

			allow, err := svc.allowPasswordResetRequest(ctx, email, clientIP)

			if allow != tc.expectAllow {
				t.Errorf("expected allow=%v, got %v", tc.expectAllow, allow)
			}
			if tc.expectErr != nil {
				if !errors.Is(err, tc.expectErr) {
					t.Errorf("expected error %v, got %v", tc.expectErr, err)
				}
			} else if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	}
}

// TestEmployeeService_AllowPasswordResetRequest_CutoffArguments asserts the
// same 15-minute-ago cutoff is passed to cleanup and both count queries, and
// that each count query is scoped to the right dimension (email vs IP).
func TestEmployeeService_AllowPasswordResetRequest_CutoffArguments(t *testing.T) {
	ctx := context.Background()
	clientIP := netip.MustParseAddr("203.0.113.7")
	email := "van-a@example.com"

	ctrl := gomock.NewController(t)
	mockRepo := mocks.NewMockQuerier(ctrl)
	mockMailer := mailermocks.NewMockClient(ctrl)
	mockOdoo := odoomocks.NewMockClient(ctrl)

	var cleanupCutoff, emailSince, ipSince pgtype.Timestamptz

	gomock.InOrder(
		mockRepo.EXPECT().LockPasswordResetRequestKey(gomock.Any(), repo.LockPasswordResetRequestKeyParams{
			Classid: passwordResetLockClassEmail,
			Key:     email,
		}).Return(nil),
		mockRepo.EXPECT().LockPasswordResetRequestKey(gomock.Any(), repo.LockPasswordResetRequestKeyParams{
			Classid: passwordResetLockClassIP,
			Key:     clientIP.String(),
		}).Return(nil),
	)

	mockRepo.EXPECT().DeletePasswordResetRequestsOlderThan(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, cutoff pgtype.Timestamptz) (int64, error) {
			cleanupCutoff = cutoff
			return 0, nil
		})
	mockRepo.EXPECT().CountPasswordResetRequestsByEmail(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, arg repo.CountPasswordResetRequestsByEmailParams) (int64, error) {
			if arg.Email != email {
				t.Errorf("expected email %q, got %q", email, arg.Email)
			}
			emailSince = arg.Since
			return 0, nil
		})
	mockRepo.EXPECT().CountPasswordResetRequestsByIPAddress(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, arg repo.CountPasswordResetRequestsByIPAddressParams) (int64, error) {
			if arg.IpAddress != clientIP {
				t.Errorf("expected IP %v, got %v", clientIP, arg.IpAddress)
			}
			ipSince = arg.Since
			return 0, nil
		})
	mockRepo.EXPECT().CreatePasswordResetRequest(gomock.Any(), repo.CreatePasswordResetRequestParams{IpAddress: clientIP, Email: email}).Return(nil)

	svc := newTestService(mockRepo, mockMailer, mockOdoo)

	if _, err := svc.allowPasswordResetRequest(ctx, email, clientIP); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !cleanupCutoff.Valid || cleanupCutoff.Time != emailSince.Time || cleanupCutoff.Time != ipSince.Time {
		t.Errorf("expected cleanup cutoff and both count 'since' arguments to share the same instant; got cleanup=%v email=%v ip=%v", cleanupCutoff, emailSince, ipSince)
	}
}
