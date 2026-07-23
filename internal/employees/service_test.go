package employees

import (
	"context"
	"errors"
	"testing"
	"time"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc/mocks"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/mailer"
	mailermocks "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/mailer/mocks"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo"
	odoomocks "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo/mocks"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/pgerr"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/tokenx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/bcrypt"
)

// mailerWaitTimeout bounds how long a test waits for the background
// sendActivationEmail goroutine (see the `go` call in service.go) to reach
// the mailer, so a stuck goroutine fails the test instead of hanging it.
const mailerWaitTimeout = time.Second

// newTestService builds a service whose withTx calls fn directly against q,
// bypassing real transaction plumbing — Create/UpdateEmployee's position-diff
// orchestration is exercised the same way regardless of what begins/commits
// the transaction. GetEmployeeByID doesn't need a transaction, so it reads
// through repo instead — set to the same mock so both styles of test can
// share one ctrl/mockRepo.
func newTestService(q repo.Querier, m mailer.Client, o odoo.Client) *service {
	return &service{
		repo: q,
		withTx: func(ctx context.Context, fn func(repo.Querier) error) error {
			return fn(q)
		},
		mailer: m,
		odoo:   o,
	}
}

func TestEmployeeService_CreateEmployee(t *testing.T) {
	ctx := context.Background()

	// Default valid parameters for testing
	defaultParams := createEmployeeParams{
		OdooEmployeeID: 30,
		FullName:       "Nguyen Van A",
		Email:          "van-a@example.com",
		Username:       "nguyenvana",
	}

	defaultRepoParams := repo.CreateEmployeeParams{
		OdooEmployeeID: defaultParams.OdooEmployeeID,
		FullName:       defaultParams.FullName,
		Email:          defaultParams.Email,
		Username:       defaultParams.Username,
	}

	// service.CreateEmployee only translates a *pgconn.PgError unique-violation
	// (code 23505) on a known constraint into a sentinel error; every other
	// error — including a plain error with matching text — passes through
	// unchanged. See translateEmployeeUniqueViolation in service.go.
	dupEmailErr := errors.New(`duplicate key value violates unique constraint "employees_email_key"`)
	dupEmployeeIDErr := errors.New(`duplicate key value violates unique constraint "employees_odoo_employee_id_key"`)
	dbErr := errors.New("connection refused")

	pgDupEmailErr := &pgconn.PgError{
		Code:           pgerr.UniqueViolation,
		ConstraintName: employeesEmailKeyConstraint,
		Message:        `duplicate key value violates unique constraint "employees_email_key"`,
	}
	pgDupEmployeeIDErr := &pgconn.PgError{
		Code:           pgerr.UniqueViolation,
		ConstraintName: employeesOdooEmployeeIDKeyConstraint,
		Message:        `duplicate key value violates unique constraint "employees_odoo_employee_id_key"`,
	}

	tests := []struct {
		name        string
		inputParams createEmployeeParams
		setupMock   func(mockRepo *mocks.MockQuerier)
		// setupMailerMock, when set, wires the mailer expectation and returns
		// a channel that closes once Send has been invoked — the activation
		// email is sent from a background goroutine (service.go), so the
		// test must wait on this before asserting mock expectations were met.
		setupMailerMock func(mockMailer *mailermocks.MockClient) <-chan struct{}
		// setupOdooMock, when set, overrides the default "Odoo confirms the
		// submitted id exists" expectation CreateEmployee always checks
		// first (ADR-0007) — used by the fail-closed test cases below.
		setupOdooMock func(mockOdoo *odoomocks.MockClient)
		expectedErr   error
		checkResponse func(t *testing.T, detail EmployeeDetail)
	}{
		{
			name:        "TC-POST-01: Create employee successfully",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{
						ID:             1,
						OdooEmployeeID: defaultParams.OdooEmployeeID,
						FullName:       defaultParams.FullName,
						Email:          defaultParams.Email,
						Username:       defaultParams.Username,
					}, nil)
				mockRepo.EXPECT().
					CreatePasswordResetToken(gomock.Any(), gomock.Any()).
					Return(repo.PasswordResetToken{ID: 1, EmployeeID: 1}, nil)
			},
			setupMailerMock: func(mockMailer *mailermocks.MockClient) <-chan struct{} {
				done := make(chan struct{})
				mockMailer.EXPECT().
					Send(gomock.Any(), defaultParams.Email, mailer.AccountActivationTemplate, gomock.Any()).
					DoAndReturn(func(context.Context, string, string, any) error {
						defer close(done)
						return nil
					})
				return done
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID == 0 {
					t.Error("Expected created employee to have a non-zero ID, but got 0")
				}
				if detail.PositionIDs == nil || len(detail.PositionIDs) != 0 {
					t.Errorf("expected empty non-nil PositionIDs, got %v", detail.PositionIDs)
				}
			},
		},
		{
			name:        "TC-POST-02: Create employee fails on duplicate email",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{}, dupEmailErr)
			},
			expectedErr: dupEmailErr,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
		{
			name:        "TC-POST-03: Create employee fails on duplicate employee ID",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{}, dupEmployeeIDErr)
			},
			expectedErr: dupEmployeeIDErr,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
		{
			name:        "TC-POST-04: Create employee fails on database error",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{}, dbErr)
			},
			expectedErr: dbErr,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
		{
			name:        "TC-POST-05: Translates a Postgres unique-violation on email to ErrEmailAlreadyExists",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{}, pgDupEmailErr)
			},
			expectedErr: ErrEmailAlreadyExists,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
		{
			name:        "TC-POST-06: Translates a Postgres unique-violation on employee ID to ErrOdooEmployeeIDAlreadyExists",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{}, pgDupEmployeeIDErr)
			},
			expectedErr: ErrOdooEmployeeIDAlreadyExists,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
		{
			name:        "TC-POST-07: Create employee succeeds but sending email fails",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{
						ID:             1,
						OdooEmployeeID: defaultParams.OdooEmployeeID,
						FullName:       defaultParams.FullName,
						Email:          defaultParams.Email,
						Username:       defaultParams.Username,
					}, nil)
				mockRepo.EXPECT().
					CreatePasswordResetToken(gomock.Any(), gomock.Any()).
					Return(repo.PasswordResetToken{ID: 1, EmployeeID: 1}, nil)
			},
			setupMailerMock: func(mockMailer *mailermocks.MockClient) <-chan struct{} {
				done := make(chan struct{})
				mockMailer.EXPECT().
					Send(gomock.Any(), defaultParams.Email, mailer.AccountActivationTemplate, gomock.Any()).
					DoAndReturn(func(context.Context, string, string, any) error {
						defer close(done)
						return errors.New("smtp: failed to send")
					})
				return done
			},
			// Employee creation must not fail just because the activation
			// email couldn't be sent — the account already exists in
			// Postgres, so CreateEmployee still returns it with a nil error.
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID == 0 {
					t.Error("Expected created employee to have a non-zero ID, but got 0")
				}
			},
		},
		{
			name:        "TC-POST-08: Create employee succeeds when mail provider returns a 5xx status",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{
						ID:             1,
						OdooEmployeeID: defaultParams.OdooEmployeeID,
						FullName:       defaultParams.FullName,
						Email:          defaultParams.Email,
						Username:       defaultParams.Username,
					}, nil)
				mockRepo.EXPECT().
					CreatePasswordResetToken(gomock.Any(), gomock.Any()).
					Return(repo.PasswordResetToken{ID: 1, EmployeeID: 1}, nil)
			},
			setupMailerMock: func(mockMailer *mailermocks.MockClient) <-chan struct{} {
				done := make(chan struct{})
				mockMailer.EXPECT().
					Send(gomock.Any(), defaultParams.Email, mailer.AccountActivationTemplate, gomock.Any()).
					DoAndReturn(func(context.Context, string, string, any) error {
						defer close(done)
						return errors.New("brevo: unexpected status 500: internal server error")
					})
				return done
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID == 0 {
					t.Error("Expected created employee to have a non-zero ID, but got 0")
				}
			},
		},
		{
			name:        "TC-POST-09: Create employee succeeds when mail provider connection fails",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{
						ID:             1,
						OdooEmployeeID: defaultParams.OdooEmployeeID,
						FullName:       defaultParams.FullName,
						Email:          defaultParams.Email,
						Username:       defaultParams.Username,
					}, nil)
				mockRepo.EXPECT().
					CreatePasswordResetToken(gomock.Any(), gomock.Any()).
					Return(repo.PasswordResetToken{ID: 1, EmployeeID: 1}, nil)
			},
			setupMailerMock: func(mockMailer *mailermocks.MockClient) <-chan struct{} {
				done := make(chan struct{})
				mockMailer.EXPECT().
					Send(gomock.Any(), defaultParams.Email, mailer.AccountActivationTemplate, gomock.Any()).
					DoAndReturn(func(context.Context, string, string, any) error {
						defer close(done)
						return errors.New("dial tcp: connection refused")
					})
				return done
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID == 0 {
					t.Error("Expected created employee to have a non-zero ID, but got 0")
				}
			},
		},
		{
			name: "TC-POST-10: Create employee with positions assigns them",
			inputParams: func() createEmployeeParams {
				p := defaultParams
				p.PositionIDs = []int64{10, 20}
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CountPositionsByIDs(gomock.Any(), []int64{10, 20}).
					Return(int64(2), nil)
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{
						ID:             1,
						OdooEmployeeID: defaultParams.OdooEmployeeID,
						FullName:       defaultParams.FullName,
						Email:          defaultParams.Email,
						Username:       defaultParams.Username,
					}, nil)
				mockRepo.EXPECT().
					InsertEmployeePositions(gomock.Any(), repo.InsertEmployeePositionsParams{
						EmployeeID:  1,
						PositionIds: []int64{10, 20},
					}).
					Return(nil)
				mockRepo.EXPECT().
					CreatePasswordResetToken(gomock.Any(), gomock.Any()).
					Return(repo.PasswordResetToken{ID: 1, EmployeeID: 1}, nil)
			},
			setupMailerMock: func(mockMailer *mailermocks.MockClient) <-chan struct{} {
				done := make(chan struct{})
				mockMailer.EXPECT().
					Send(gomock.Any(), defaultParams.Email, mailer.AccountActivationTemplate, gomock.Any()).
					DoAndReturn(func(context.Context, string, string, any) error {
						defer close(done)
						return nil
					})
				return done
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if len(detail.PositionIDs) != 2 {
					t.Errorf("expected 2 position ids, got %v", detail.PositionIDs)
				}
			},
		},
		{
			name: "TC-POST-11: Create employee with an unknown position id fails closed with ErrUnknownPositionID",
			inputParams: func() createEmployeeParams {
				p := defaultParams
				p.PositionIDs = []int64{10, 999}
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CountPositionsByIDs(gomock.Any(), []int64{10, 999}).
					Return(int64(1), nil)
				// No CreateEmployee/InsertEmployeePositions expectation: the
				// mock controller fails the test if either is called, proving
				// validation runs before the employee row is written.
			},
			expectedErr: ErrUnknownPositionID,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
		{
			name: "TC-POST-12: Create employee with an explicit empty position set has empty (non-nil) PositionIDs",
			inputParams: func() createEmployeeParams {
				p := defaultParams
				p.PositionIDs = []int64{}
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				// No CountPositionsByIDs/InsertEmployeePositions expectation:
				// an empty submitted set short-circuits both.
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{
						ID:             1,
						OdooEmployeeID: defaultParams.OdooEmployeeID,
						FullName:       defaultParams.FullName,
						Email:          defaultParams.Email,
						Username:       defaultParams.Username,
					}, nil)
				mockRepo.EXPECT().
					CreatePasswordResetToken(gomock.Any(), gomock.Any()).
					Return(repo.PasswordResetToken{ID: 1, EmployeeID: 1}, nil)
			},
			setupMailerMock: func(mockMailer *mailermocks.MockClient) <-chan struct{} {
				done := make(chan struct{})
				mockMailer.EXPECT().
					Send(gomock.Any(), defaultParams.Email, mailer.AccountActivationTemplate, gomock.Any()).
					DoAndReturn(func(context.Context, string, string, any) error {
						defer close(done)
						return nil
					})
				return done
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.PositionIDs == nil || len(detail.PositionIDs) != 0 {
					t.Errorf("expected empty non-nil PositionIDs, got %v", detail.PositionIDs)
				}
			},
		},
		{
			name:        "TC-POST-13: Create employee rejected when Odoo doesn't confirm the id exists",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				// No CreateEmployee expectation: the mock controller fails
				// the test if it's called, proving the Odoo existence check
				// runs before any write.
			},
			setupOdooMock: func(mockOdoo *odoomocks.MockClient) {
				mockOdoo.EXPECT().
					FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{defaultParams.OdooEmployeeID}).
					Return([]odoo.Employee{}, nil)
			},
			expectedErr: ErrOdooEmployeeIDNotFound,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
		{
			name:        "TC-POST-14: Create employee rejected when the Odoo existence check itself fails",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				// No CreateEmployee expectation: an unreachable Odoo must
				// fail closed, never letting the write through.
			},
			setupOdooMock: func(mockOdoo *odoomocks.MockClient) {
				mockOdoo.EXPECT().
					FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{defaultParams.OdooEmployeeID}).
					Return(nil, errors.New("odoo: connection refused"))
			},
			expectedErr: ErrOdooEmployeeIDNotFound,
		},
		{
			name: "TC-POST-15: Create employee whose position is deleted in the race window between validation and insert fails closed with ErrUnknownPositionID",
			inputParams: func() createEmployeeParams {
				p := defaultParams
				p.PositionIDs = []int64{10, 20}
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CountPositionsByIDs(gomock.Any(), []int64{10, 20}).
					Return(int64(2), nil)
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{
						ID:             1,
						OdooEmployeeID: defaultParams.OdooEmployeeID,
						FullName:       defaultParams.FullName,
						Email:          defaultParams.Email,
						Username:       defaultParams.Username,
					}, nil)
				mockRepo.EXPECT().
					InsertEmployeePositions(gomock.Any(), repo.InsertEmployeePositionsParams{
						EmployeeID:  1,
						PositionIds: []int64{10, 20},
					}).
					Return(&pgconn.PgError{
						Code:           pgerr.ForeignKeyViolation,
						ConstraintName: "employee_positions_position_id_fkey",
						Message:        `insert or update on table "employee_positions" violates foreign key constraint "employee_positions_position_id_fkey"`,
					})
			},
			expectedErr: ErrUnknownPositionID,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			mockMailer := mailermocks.NewMockClient(ctrl)
			mockOdoo := odoomocks.NewMockClient(ctrl)

			tc.setupMock(mockRepo)
			if tc.setupOdooMock != nil {
				tc.setupOdooMock(mockOdoo)
			} else {
				mockOdoo.EXPECT().
					FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{tc.inputParams.OdooEmployeeID}).
					Return([]odoo.Employee{{OdooEmployeeID: tc.inputParams.OdooEmployeeID}}, nil)
			}
			var mailerDone <-chan struct{}
			if tc.setupMailerMock != nil {
				mailerDone = tc.setupMailerMock(mockMailer)
			}

			svc := newTestService(mockRepo, mockMailer, mockOdoo)

			// Execute
			detail, err := svc.CreateEmployee(ctx, tc.inputParams)

			if mailerDone != nil {
				select {
				case <-mailerDone:
				case <-time.After(mailerWaitTimeout):
					t.Fatal("timed out waiting for the background activation email to be sent")
				}
			}

			// Assert
			if tc.expectedErr != nil {
				if err == nil {
					t.Fatalf("Expected error '%v', but got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("Expected error '%v', but got '%v'", tc.expectedErr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error, but got: %v", err)
				}
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, detail)
			}
		})
	}
}

func TestEmployeeService_ListEmployees(t *testing.T) {
	ctx := context.Background()

	dbErr := errors.New("connection refused")

	tests := []struct {
		name          string
		setupMock     func(mockRepo *mocks.MockQuerier)
		expectedErr   error
		checkResponse func(t *testing.T, details []EmployeeDetail)
	}{
		{
			name: "TC-LIST-01: List employees successfully",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					ListEmployees(gomock.Any(), gomock.Any()).
					Return([]repo.Employee{
						{ID: 1, OdooEmployeeID: 30, FullName: "Nguyen Van A"},
						{ID: 2, OdooEmployeeID: 31, FullName: "Tran Thi B"},
					}, nil)
				mockRepo.EXPECT().
					ListPositionIDsByEmployeeIDs(gomock.Any(), []int64{1, 2}).
					Return([]repo.EmployeePosition{}, nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeIDs(gomock.Any(), []int64{1, 2}).
					Return([]repo.EmployeeStore{}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, details []EmployeeDetail) {
				if len(details) != 2 {
					t.Fatalf("expected 2 employees, got %d", len(details))
				}
			},
		},
		{
			name: "TC-LIST-02: List employees returns an empty slice when there are none",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					ListEmployees(gomock.Any(), gomock.Any()).
					Return([]repo.Employee{}, nil)
				mockRepo.EXPECT().
					ListPositionIDsByEmployeeIDs(gomock.Any(), []int64{}).
					Return([]repo.EmployeePosition{}, nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeIDs(gomock.Any(), []int64{}).
					Return([]repo.EmployeeStore{}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, details []EmployeeDetail) {
				if len(details) != 0 {
					t.Fatalf("expected 0 employees, got %d", len(details))
				}
			},
		},
		{
			name: "TC-LIST-03: List employees fails on database error",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					ListEmployees(gomock.Any(), gomock.Any()).
					Return(nil, dbErr)
			},
			expectedErr: dbErr,
			checkResponse: func(t *testing.T, details []EmployeeDetail) {
				if details != nil {
					t.Errorf("expected nil details when an error occurs, got %v", details)
				}
			},
		},
		{
			name: "TC-LIST-04: Includes each employee's own position ids, non-nil when empty",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					ListEmployees(gomock.Any(), gomock.Any()).
					Return([]repo.Employee{
						{ID: 1, OdooEmployeeID: 30, FullName: "Nguyen Van A"},
						{ID: 2, OdooEmployeeID: 31, FullName: "Tran Thi B"},
					}, nil)
				mockRepo.EXPECT().
					ListPositionIDsByEmployeeIDs(gomock.Any(), []int64{1, 2}).
					Return([]repo.EmployeePosition{
						{EmployeeID: 1, PositionID: 10},
						{EmployeeID: 1, PositionID: 20},
					}, nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeIDs(gomock.Any(), []int64{1, 2}).
					Return([]repo.EmployeeStore{
						{EmployeeID: 1, StoreID: 100},
					}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, details []EmployeeDetail) {
				if len(details) != 2 {
					t.Fatalf("expected 2 employees, got %d", len(details))
				}
				if len(details[0].PositionIDs) != 2 {
					t.Errorf("expected employee 1 to have 2 position ids, got %v", details[0].PositionIDs)
				}
				if details[1].PositionIDs == nil || len(details[1].PositionIDs) != 0 {
					t.Errorf("expected employee 2 to have empty non-nil PositionIDs, got %v", details[1].PositionIDs)
				}
			},
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

			details, err := svc.ListEmployees(ctx, ListEmployeesFilter{})

			if tc.expectedErr != nil {
				if err == nil {
					t.Fatalf("Expected error '%v', but got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("Expected error '%v', but got '%v'", tc.expectedErr, err)
				}
			} else if err != nil {
				t.Fatalf("Expected no error, but got: %v", err)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, details)
			}
		})
	}
}

func TestEmployeeService_GetEmployeeByID(t *testing.T) {
	ctx := context.Background()

	dbErr := errors.New("connection refused")

	tests := []struct {
		name          string
		inputID       int64
		setupMock     func(mockRepo *mocks.MockQuerier)
		expectedErr   error
		checkResponse func(t *testing.T, detail EmployeeDetail)
	}{
		{
			name:    "TC-GET-01: Get employee by ID successfully",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: 30, FullName: "Nguyen Van A"}, nil)
				mockRepo.EXPECT().
					ListPositionIDsByEmployeeID(gomock.Any(), int64(1)).
					Return([]int64{10, 20}, nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).
					Return([]int64{100}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 1 {
					t.Errorf("expected employee ID 1, got %d", detail.Employee.ID)
				}
				if len(detail.PositionIDs) != 2 {
					t.Errorf("expected 2 position ids, got %v", detail.PositionIDs)
				}
			},
		},
		{
			name:    "TC-GET-02: Translates a no-rows repo error to ErrEmployeeNotFound",
			inputID: 999,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(999)).
					Return(repo.Employee{}, pgx.ErrNoRows)
			},
			expectedErr: ErrEmployeeNotFound,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
		{
			name:    "TC-GET-03: Get employee by ID fails on database error",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{}, dbErr)
			},
			expectedErr: dbErr,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
		{
			name:    "TC-GET-04: Get employee with no positions has empty (non-nil) PositionIDs",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: 30, FullName: "Nguyen Van A"}, nil)
				mockRepo.EXPECT().
					ListPositionIDsByEmployeeID(gomock.Any(), int64(1)).
					Return([]int64{}, nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).
					Return([]int64{}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.PositionIDs == nil || len(detail.PositionIDs) != 0 {
					t.Errorf("expected empty non-nil PositionIDs, got %v", detail.PositionIDs)
				}
			},
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

			detail, err := svc.GetEmployeeByID(ctx, tc.inputID)

			if tc.expectedErr != nil {
				if err == nil {
					t.Fatalf("Expected error '%v', but got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("Expected error '%v', but got '%v'", tc.expectedErr, err)
				}
			} else if err != nil {
				t.Fatalf("Expected no error, but got: %v", err)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, detail)
			}
		})
	}
}

func TestEmployeeService_UpdateEmployee(t *testing.T) {
	ctx := context.Background()

	inputParams := updateEmployeeParams{
		OdooEmployeeID: 1,
		FullName:       "Nguyen Van A",
		Email:          "van-a@example.com",
		Username:       "nguyenvana",
	}
	repoParams := repo.UpdateEmployeeParams{
		ID:             1,
		OdooEmployeeID: inputParams.OdooEmployeeID,
		FullName:       inputParams.FullName,
		Email:          inputParams.Email,
		Username:       inputParams.Username,
	}

	dupEmailErr := &pgconn.PgError{
		Code:           pgerr.UniqueViolation,
		ConstraintName: employeesEmailKeyConstraint,
		Message:        `duplicate key value violates unique constraint "employees_email_key"`,
	}
	dupUsernameErr := &pgconn.PgError{
		Code:           pgerr.UniqueViolation,
		ConstraintName: employeesUsernameKeyConstraint,
		Message:        `duplicate key value violates unique constraint "employees_username_key"`,
	}
	dupEmployeeIDErr := &pgconn.PgError{
		Code:           pgerr.UniqueViolation,
		ConstraintName: employeesOdooEmployeeIDKeyConstraint,
		Message:        `duplicate key value violates unique constraint "employees_odoo_employee_id_key"`,
	}
	dbErr := errors.New("connection refused")

	tests := []struct {
		name      string
		params    updateEmployeeParams
		setupMock func(mockRepo *mocks.MockQuerier)
		// setupOdooMock, when set, overrides the default "no Odoo call
		// expected" assumption — used by the cases below that change
		// odooEmployeeId and so must re-validate against Odoo (ADR-0007).
		setupOdooMock func(mockOdoo *odoomocks.MockClient)
		expectedErr   error
		checkResponse func(t *testing.T, detail EmployeeDetail)
	}{
		{
			name:   "TC-PUT-01: Update employee successfully",
			params: inputParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{
						ID:       1,
						FullName: inputParams.FullName,
						Email:    inputParams.Email,
						Username: inputParams.Username,
					}, nil)
				mockRepo.EXPECT().
					DeleteEmployeePositionsNotIn(gomock.Any(), repo.DeleteEmployeePositionsNotInParams{
						EmployeeID:  1,
						PositionIds: []int64{},
					}).
					Return(nil)
				mockRepo.EXPECT().
					InsertEmployeePositions(gomock.Any(), repo.InsertEmployeePositionsParams{
						EmployeeID:  1,
						PositionIds: []int64{},
					}).
					Return(nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).
					Return([]int64{}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.FullName != inputParams.FullName {
					t.Errorf("expected full name %q, got %q", inputParams.FullName, detail.Employee.FullName)
				}
				if detail.PositionIDs == nil || len(detail.PositionIDs) != 0 {
					t.Errorf("expected empty non-nil PositionIDs, got %v", detail.PositionIDs)
				}
			},
		},
		{
			name:   "TC-PUT-02: Translates a no-rows repo error from the update itself to ErrEmployeeNotFound",
			params: inputParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				// The initial GetEmployeeByID lookup still finds the row —
				// this exercises the race where it's gone by the time the
				// actual UPDATE runs (e.g. deleted concurrently), a
				// different path than the id never existing at all (see
				// TC-PUT-09).
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{}, pgx.ErrNoRows)
			},
			expectedErr: ErrEmployeeNotFound,
		},
		{
			name:   "TC-PUT-03: Translates a Postgres unique-violation on email to ErrEmailAlreadyExists",
			params: inputParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{}, dupEmailErr)
			},
			expectedErr: ErrEmailAlreadyExists,
		},
		{
			name:   "TC-PUT-04: Translates a Postgres unique-violation on username to ErrUsernameAlreadyExists",
			params: inputParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{}, dupUsernameErr)
			},
			expectedErr: ErrUsernameAlreadyExists,
		},
		{
			name:   "TC-PUT-04b: Translates a Postgres unique-violation on employee_id to ErrOdooEmployeeIDAlreadyExists",
			params: inputParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{}, dupEmployeeIDErr)
			},
			expectedErr: ErrOdooEmployeeIDAlreadyExists,
		},
		{
			name:   "TC-PUT-05: Update employee fails on database error",
			params: inputParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{}, dbErr)
			},
			expectedErr: dbErr,
		},
		{
			name: "TC-PUT-06: Update employee replaces its position set via diff",
			params: func() updateEmployeeParams {
				p := inputParams
				p.PositionIDs = []int64{10, 20}
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					CountPositionsByIDs(gomock.Any(), []int64{10, 20}).
					Return(int64(2), nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{
						ID:       1,
						FullName: inputParams.FullName,
						Email:    inputParams.Email,
						Username: inputParams.Username,
					}, nil)
				mockRepo.EXPECT().
					DeleteEmployeePositionsNotIn(gomock.Any(), repo.DeleteEmployeePositionsNotInParams{
						EmployeeID:  1,
						PositionIds: []int64{10, 20},
					}).
					Return(nil)
				mockRepo.EXPECT().
					InsertEmployeePositions(gomock.Any(), repo.InsertEmployeePositionsParams{
						EmployeeID:  1,
						PositionIds: []int64{10, 20},
					}).
					Return(nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).
					Return([]int64{}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if len(detail.PositionIDs) != 2 {
					t.Errorf("expected 2 position ids, got %v", detail.PositionIDs)
				}
			},
		},
		{
			name: "TC-PUT-07: Update employee with an unknown position id fails closed with ErrUnknownPositionID",
			params: func() updateEmployeeParams {
				p := inputParams
				p.PositionIDs = []int64{10, 999}
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					CountPositionsByIDs(gomock.Any(), []int64{10, 999}).
					Return(int64(1), nil)
				// No UpdateEmployee/Delete/InsertEmployeePositions expectation:
				// the mock controller fails the test if any is called, proving
				// validation runs before any write.
			},
			expectedErr: ErrUnknownPositionID,
		},
		{
			name: "TC-PUT-08: Update employee with an explicit empty position set clears assignments",
			params: func() updateEmployeeParams {
				p := inputParams
				p.PositionIDs = []int64{}
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				// No CountPositionsByIDs expectation: an empty submitted set
				// short-circuits validatePositionIDs. InsertEmployeePositions
				// still runs (dbx.DiffReplace always calls it) — the
				// underlying sqlc INSERT...SELECT unnest([])...ON CONFLICT DO
				// NOTHING is already a safe no-op over an empty set.
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{
						ID:       1,
						FullName: inputParams.FullName,
						Email:    inputParams.Email,
						Username: inputParams.Username,
					}, nil)
				mockRepo.EXPECT().
					DeleteEmployeePositionsNotIn(gomock.Any(), repo.DeleteEmployeePositionsNotInParams{
						EmployeeID:  1,
						PositionIds: []int64{},
					}).
					Return(nil)
				mockRepo.EXPECT().
					InsertEmployeePositions(gomock.Any(), repo.InsertEmployeePositionsParams{
						EmployeeID:  1,
						PositionIds: []int64{},
					}).
					Return(nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).
					Return([]int64{}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.PositionIDs == nil || len(detail.PositionIDs) != 0 {
					t.Errorf("expected empty non-nil PositionIDs, got %v", detail.PositionIDs)
				}
			},
		},
		{
			name:   "TC-PUT-09: Unknown employee id (initial lookup) maps to ErrEmployeeNotFound without touching Odoo",
			params: inputParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{}, pgx.ErrNoRows)
				// No UpdateEmployee expectation: the lookup failing must
				// short-circuit before any write is attempted.
			},
			expectedErr: ErrEmployeeNotFound,
		},
		{
			name:   "TC-PUT-10: Initial lookup fails on a database error",
			params: inputParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{}, dbErr)
			},
			expectedErr: dbErr,
		},
		{
			name: "TC-PUT-11: Changing odooEmployeeId re-validates against Odoo and succeeds",
			params: func() updateEmployeeParams {
				p := inputParams
				p.OdooEmployeeID = 2
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repo.UpdateEmployeeParams{
						ID:             1,
						OdooEmployeeID: 2,
						FullName:       inputParams.FullName,
						Email:          inputParams.Email,
						Username:       inputParams.Username,
					}).
					Return(repo.Employee{ID: 1, OdooEmployeeID: 2}, nil)
				mockRepo.EXPECT().
					DeleteEmployeePositionsNotIn(gomock.Any(), repo.DeleteEmployeePositionsNotInParams{
						EmployeeID:  1,
						PositionIds: []int64{},
					}).
					Return(nil)
				mockRepo.EXPECT().
					InsertEmployeePositions(gomock.Any(), repo.InsertEmployeePositionsParams{
						EmployeeID:  1,
						PositionIds: []int64{},
					}).
					Return(nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).
					Return([]int64{}, nil)
			},
			setupOdooMock: func(mockOdoo *odoomocks.MockClient) {
				mockOdoo.EXPECT().
					FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{2}).
					Return([]odoo.Employee{{OdooEmployeeID: 2}}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, detail EmployeeDetail) {
				if detail.Employee.OdooEmployeeID != 2 {
					t.Errorf("expected OdooEmployeeID 2, got %d", detail.Employee.OdooEmployeeID)
				}
			},
		},
		{
			name: "TC-PUT-12: Changing odooEmployeeId to one Odoo doesn't confirm fails closed",
			params: func() updateEmployeeParams {
				p := inputParams
				p.OdooEmployeeID = 2
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				// No UpdateEmployee/position expectation: Odoo not
				// confirming the new id must block the write entirely.
			},
			setupOdooMock: func(mockOdoo *odoomocks.MockClient) {
				mockOdoo.EXPECT().
					FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{2}).
					Return([]odoo.Employee{}, nil)
			},
			expectedErr: ErrOdooEmployeeIDNotFound,
		},
		{
			name: "TC-PUT-13: Changing odooEmployeeId fails closed when the Odoo check itself errors",
			params: func() updateEmployeeParams {
				p := inputParams
				p.OdooEmployeeID = 2
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
			},
			setupOdooMock: func(mockOdoo *odoomocks.MockClient) {
				mockOdoo.EXPECT().
					FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{2}).
					Return(nil, errors.New("odoo: connection refused"))
			},
			expectedErr: ErrOdooEmployeeIDNotFound,
		},
		{
			name: "TC-PUT-14: Update employee whose position is deleted in the race window between validation and insert fails closed with ErrUnknownPositionID",
			params: func() updateEmployeeParams {
				p := inputParams
				p.PositionIDs = []int64{10, 20}
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: inputParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					CountPositionsByIDs(gomock.Any(), []int64{10, 20}).
					Return(int64(2), nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{
						ID:       1,
						FullName: inputParams.FullName,
						Email:    inputParams.Email,
						Username: inputParams.Username,
					}, nil)
				mockRepo.EXPECT().
					DeleteEmployeePositionsNotIn(gomock.Any(), repo.DeleteEmployeePositionsNotInParams{
						EmployeeID:  1,
						PositionIds: []int64{10, 20},
					}).
					Return(nil)
				mockRepo.EXPECT().
					InsertEmployeePositions(gomock.Any(), repo.InsertEmployeePositionsParams{
						EmployeeID:  1,
						PositionIds: []int64{10, 20},
					}).
					Return(&pgconn.PgError{
						Code:           pgerr.ForeignKeyViolation,
						ConstraintName: "employee_positions_position_id_fkey",
						Message:        `insert or update on table "employee_positions" violates foreign key constraint "employee_positions_position_id_fkey"`,
					})
			},
			expectedErr: ErrUnknownPositionID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			mockMailer := mailermocks.NewMockClient(ctrl)
			mockOdoo := odoomocks.NewMockClient(ctrl)

			tc.setupMock(mockRepo)
			if tc.setupOdooMock != nil {
				tc.setupOdooMock(mockOdoo)
			}
			// No default Odoo expectation here (unlike CreateEmployee's
			// loop): most cases leave odooEmployeeId unchanged, and
			// UpdateEmployee must not call Odoo at all in that case — an
			// unexpected call would fail the test via the mock controller.

			svc := newTestService(mockRepo, mockMailer, mockOdoo)

			detail, err := svc.UpdateEmployee(ctx, 1, tc.params)

			if tc.expectedErr != nil {
				if err == nil {
					t.Fatalf("Expected error '%v', but got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("Expected error '%v', but got '%v'", tc.expectedErr, err)
				}
			} else if err != nil {
				t.Fatalf("Expected no error, but got: %v", err)
			}

			if tc.checkResponse != nil {
				tc.checkResponse(t, detail)
			}
		})
	}
}

func TestEmployeeService_UpdateEmployee_Password(t *testing.T) {
	ctx := context.Background()

	baseParams := updateEmployeeParams{
		OdooEmployeeID: 1,
		FullName:       "Nguyen Van A",
		Email:          "van-a@example.com",
		Username:       "nguyenvana",
	}
	repoParams := repo.UpdateEmployeeParams{
		ID:             1,
		OdooEmployeeID: baseParams.OdooEmployeeID,
		FullName:       baseParams.FullName,
		Email:          baseParams.Email,
		Username:       baseParams.Username,
	}
	dbErr := errors.New("connection refused")

	tests := []struct {
		name        string
		params      updateEmployeeParams
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
	}{
		{
			name: "TC-PUT-09: Nil password leaves the password column untouched",
			params: func() updateEmployeeParams {
				p := baseParams
				p.Password = nil
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: baseParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{ID: 1}, nil)
				mockRepo.EXPECT().
					DeleteEmployeePositionsNotIn(gomock.Any(), gomock.Any()).
					Return(nil)
				mockRepo.EXPECT().
					InsertEmployeePositions(gomock.Any(), gomock.Any()).
					Return(nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).
					Return([]int64{}, nil)
				mockRepo.EXPECT().SetEmployeePassword(gomock.Any(), gomock.Any()).Times(0)
			},
			expectedErr: nil,
		},
		{
			name: "TC-PUT-10: Non-nil password bcrypt-hashes and sets it via SetEmployeePassword",
			params: func() updateEmployeeParams {
				p := baseParams
				pw := "supersecret"
				p.Password = &pw
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: baseParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{ID: 1}, nil)
				mockRepo.EXPECT().
					DeleteEmployeePositionsNotIn(gomock.Any(), gomock.Any()).
					Return(nil)
				mockRepo.EXPECT().
					InsertEmployeePositions(gomock.Any(), gomock.Any()).
					Return(nil)
				mockRepo.EXPECT().
					ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).
					Return([]int64{}, nil)
				mockRepo.EXPECT().
					SetEmployeePassword(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, arg repo.SetEmployeePasswordParams) (int64, error) {
						if arg.ID != 1 {
							t.Errorf("expected SetEmployeePassword for employee 1, got %d", arg.ID)
						}
						if err := bcrypt.CompareHashAndPassword(arg.Password, []byte("supersecret")); err != nil {
							t.Errorf("expected Password to be a bcrypt hash of the input password: %v", err)
						}
						return 1, nil
					})
			},
			expectedErr: nil,
		},
		{
			name: "TC-PUT-11: Propagates an error from SetEmployeePassword",
			params: func() updateEmployeeParams {
				p := baseParams
				pw := "supersecret"
				p.Password = &pw
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, OdooEmployeeID: baseParams.OdooEmployeeID}, nil)
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{ID: 1}, nil)
				mockRepo.EXPECT().
					DeleteEmployeePositionsNotIn(gomock.Any(), gomock.Any()).
					Return(nil)
				mockRepo.EXPECT().
					InsertEmployeePositions(gomock.Any(), gomock.Any()).
					Return(nil)
				// SetEmployeePassword now runs inside the same transaction as
				// the rest of the update, before ListStoreIDsByEmployeeID —
				// its error aborts the transaction, so ListStoreIDsByEmployeeID
				// is never reached.
				mockRepo.EXPECT().
					SetEmployeePassword(gomock.Any(), gomock.Any()).
					Return(int64(0), dbErr)
			},
			expectedErr: dbErr,
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

			_, err := svc.UpdateEmployee(ctx, 1, tc.params)

			if tc.expectedErr != nil {
				if err == nil {
					t.Fatalf("Expected error '%v', but got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("Expected error '%v', but got '%v'", tc.expectedErr, err)
				}
			} else if err != nil {
				t.Fatalf("Expected no error, but got: %v", err)
			}
		})
	}
}

func TestEmployeeService_SetEmployeeActive(t *testing.T) {
	ctx := context.Background()

	dbErr := errors.New("connection refused")

	tests := []struct {
		name        string
		inputID     int64
		inputActive bool
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
	}{
		{
			name:        "TC-ACTIVE-01: Deactivate an existing employee",
			inputID:     1,
			inputActive: false,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					SetEmployeeActive(gomock.Any(), repo.SetEmployeeActiveParams{ID: 1, IsActive: false}).
					Return(int64(1), nil)
			},
			expectedErr: nil,
		},
		{
			name:        "TC-ACTIVE-02: Activate an existing employee",
			inputID:     1,
			inputActive: true,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					SetEmployeeActive(gomock.Any(), repo.SetEmployeeActiveParams{ID: 1, IsActive: true}).
					Return(int64(1), nil)
			},
			expectedErr: nil,
		},
		{
			name:        "TC-ACTIVE-03: Setting active status on an already-active/inactive employee is idempotent",
			inputID:     1,
			inputActive: false,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					SetEmployeeActive(gomock.Any(), repo.SetEmployeeActiveParams{ID: 1, IsActive: false}).
					Return(int64(1), nil)
			},
			expectedErr: nil,
		},
		{
			name:        "TC-ACTIVE-04: Unknown id translates zero rows affected to ErrEmployeeNotFound",
			inputID:     999,
			inputActive: true,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					SetEmployeeActive(gomock.Any(), repo.SetEmployeeActiveParams{ID: 999, IsActive: true}).
					Return(int64(0), nil)
			},
			expectedErr: ErrEmployeeNotFound,
		},
		{
			name:        "TC-ACTIVE-05: Fails on database error",
			inputID:     1,
			inputActive: true,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					SetEmployeeActive(gomock.Any(), repo.SetEmployeeActiveParams{ID: 1, IsActive: true}).
					Return(int64(0), dbErr)
			},
			expectedErr: dbErr,
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

			err := svc.SetEmployeeActive(ctx, tc.inputID, tc.inputActive)

			if tc.expectedErr != nil {
				if err == nil {
					t.Fatalf("Expected error '%v', but got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("Expected error '%v', but got '%v'", tc.expectedErr, err)
				}
			} else if err != nil {
				t.Fatalf("Expected no error, but got: %v", err)
			}
		})
	}
}

func TestEmployeeService_SetEmployeePassword(t *testing.T) {
	ctx := context.Background()

	dbErr := errors.New("connection refused")

	tests := []struct {
		name        string
		inputID     int64
		inputPass   string
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
	}{
		{
			name:      "TC-SETPW-01: Sets a bcrypt-hashed password for an existing employee",
			inputID:   1,
			inputPass: "supersecret",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					SetEmployeePassword(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, arg repo.SetEmployeePasswordParams) (int64, error) {
						if arg.ID != 1 {
							t.Errorf("expected SetEmployeePassword for employee 1, got %d", arg.ID)
						}
						if err := bcrypt.CompareHashAndPassword(arg.Password, []byte("supersecret")); err != nil {
							t.Errorf("expected Password to be a bcrypt hash of the input password: %v", err)
						}
						return 1, nil
					})
			},
			expectedErr: nil,
		},
		{
			name:      "TC-SETPW-02: Unknown id translates zero rows affected to ErrEmployeeNotFound",
			inputID:   999,
			inputPass: "supersecret",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					SetEmployeePassword(gomock.Any(), gomock.Any()).
					Return(int64(0), nil)
			},
			expectedErr: ErrEmployeeNotFound,
		},
		{
			name:      "TC-SETPW-03: Fails on database error",
			inputID:   1,
			inputPass: "supersecret",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					SetEmployeePassword(gomock.Any(), gomock.Any()).
					Return(int64(0), dbErr)
			},
			expectedErr: dbErr,
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

			err := svc.SetEmployeePassword(ctx, tc.inputID, tc.inputPass)

			if tc.expectedErr != nil {
				if err == nil {
					t.Fatalf("Expected error '%v', but got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("Expected error '%v', but got '%v'", tc.expectedErr, err)
				}
			} else if err != nil {
				t.Fatalf("Expected no error, but got: %v", err)
			}
		})
	}
}

func TestEmployeeService_DeleteEmployee(t *testing.T) {
	ctx := context.Background()

	dbErr := errors.New("connection refused")

	tests := []struct {
		name        string
		inputID     int64
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
	}{
		{
			name:    "TC-DEL-01: Delete an existing employee",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					DeleteEmployee(gomock.Any(), int64(1)).
					Return(int64(1), nil)
			},
			expectedErr: nil,
		},
		{
			name:    "TC-DEL-02: Unknown id translates zero rows affected to ErrEmployeeNotFound",
			inputID: 999,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					DeleteEmployee(gomock.Any(), int64(999)).
					Return(int64(0), nil)
			},
			expectedErr: ErrEmployeeNotFound,
		},
		{
			name:    "TC-DEL-03: Fails on database error",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					DeleteEmployee(gomock.Any(), int64(1)).
					Return(int64(0), dbErr)
			},
			expectedErr: dbErr,
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

			err := svc.DeleteEmployee(ctx, tc.inputID)

			if tc.expectedErr != nil {
				if err == nil {
					t.Fatalf("Expected error '%v', but got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("Expected error '%v', but got '%v'", tc.expectedErr, err)
				}
			} else if err != nil {
				t.Fatalf("Expected no error, but got: %v", err)
			}
		})
	}
}

func TestEmployeeService_BulkDeleteEmployees(t *testing.T) {
	ctx := context.Background()

	dbErr := errors.New("connection refused")

	tests := []struct {
		name      string
		inputIDs  []int64
		setupMock func(mockRepo *mocks.MockQuerier)
		expected  []BulkActionResult
	}{
		{
			name:     "TC-BULKDEL-01: All ids delete successfully",
			inputIDs: []int64{1, 2},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().DeleteEmployee(gomock.Any(), int64(1)).Return(int64(1), nil)
				mockRepo.EXPECT().DeleteEmployee(gomock.Any(), int64(2)).Return(int64(1), nil)
			},
			expected: []BulkActionResult{
				{ID: 1, Success: true},
				{ID: 2, Success: true},
			},
		},
		{
			name:     "TC-BULKDEL-02: One unknown id is reported without blocking the others",
			inputIDs: []int64{1, 999, 2},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().DeleteEmployee(gomock.Any(), int64(1)).Return(int64(1), nil)
				mockRepo.EXPECT().DeleteEmployee(gomock.Any(), int64(999)).Return(int64(0), nil)
				mockRepo.EXPECT().DeleteEmployee(gomock.Any(), int64(2)).Return(int64(1), nil)
			},
			expected: []BulkActionResult{
				{ID: 1, Success: true},
				{ID: 999, Success: false, Error: ErrEmployeeNotFound.Error()},
				{ID: 2, Success: true},
			},
		},
		{
			name:     "TC-BULKDEL-03: A generic repo error on one id is reported without blocking the others",
			inputIDs: []int64{1, 2},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().DeleteEmployee(gomock.Any(), int64(1)).Return(int64(0), dbErr)
				mockRepo.EXPECT().DeleteEmployee(gomock.Any(), int64(2)).Return(int64(1), nil)
			},
			expected: []BulkActionResult{
				{ID: 1, Success: false, Error: dbErr.Error()},
				{ID: 2, Success: true},
			},
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

			results := svc.BulkDeleteEmployees(ctx, tc.inputIDs)

			if len(results) != len(tc.expected) {
				t.Fatalf("expected %d results, got %d", len(tc.expected), len(results))
			}
			for i, want := range tc.expected {
				got := results[i]
				if got.ID != want.ID || got.Success != want.Success || got.Error != want.Error {
					t.Errorf("result[%d]: expected %+v, got %+v", i, want, got)
				}
			}
		})
	}
}

func TestEmployeeService_BulkSendPasswordResetLinks(t *testing.T) {
	ctx := context.Background()

	activeEmployee := repo.Employee{ID: 1, FullName: "Nguyen Van A", Email: "van-a@example.com", IsActive: true}
	inactiveEmployee := repo.Employee{ID: 2, FullName: "Tran Thi B", Email: "tran-b@example.com", IsActive: false}
	dbErr := errors.New("connection refused")

	tests := []struct {
		name      string
		inputIDs  []int64
		setupMock func(mockRepo *mocks.MockQuerier, mockMailer *mailermocks.MockClient)
		expected  []BulkActionResult
	}{
		{
			name:     "TC-BULKSEND-01: Sends a link to an active employee successfully",
			inputIDs: []int64{1},
			setupMock: func(mockRepo *mocks.MockQuerier, mockMailer *mailermocks.MockClient) {
				mockRepo.EXPECT().GetEmployeeByID(gomock.Any(), int64(1)).Return(activeEmployee, nil)
				mockRepo.EXPECT().ListPositionIDsByEmployeeID(gomock.Any(), int64(1)).Return([]int64{}, nil)
				mockRepo.EXPECT().ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).Return([]int64{}, nil)
				mockRepo.EXPECT().CreatePasswordResetToken(gomock.Any(), gomock.Any()).Return(repo.PasswordResetToken{ID: 1, EmployeeID: 1}, nil)
				mockMailer.EXPECT().Send(gomock.Any(), activeEmployee.Email, mailer.PasswordResetTemplate, gomock.Any()).Return(nil)
			},
			expected: []BulkActionResult{{ID: 1, Success: true}},
		},
		{
			name:     "TC-BULKSEND-02: Unknown id is reported without blocking the others",
			inputIDs: []int64{999, 1},
			setupMock: func(mockRepo *mocks.MockQuerier, mockMailer *mailermocks.MockClient) {
				mockRepo.EXPECT().GetEmployeeByID(gomock.Any(), int64(999)).Return(repo.Employee{}, pgx.ErrNoRows)
				mockRepo.EXPECT().GetEmployeeByID(gomock.Any(), int64(1)).Return(activeEmployee, nil)
				mockRepo.EXPECT().ListPositionIDsByEmployeeID(gomock.Any(), int64(1)).Return([]int64{}, nil)
				mockRepo.EXPECT().ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).Return([]int64{}, nil)
				mockRepo.EXPECT().CreatePasswordResetToken(gomock.Any(), gomock.Any()).Return(repo.PasswordResetToken{ID: 1, EmployeeID: 1}, nil)
				mockMailer.EXPECT().Send(gomock.Any(), activeEmployee.Email, mailer.PasswordResetTemplate, gomock.Any()).Return(nil)
			},
			expected: []BulkActionResult{
				{ID: 999, Success: false, Error: ErrEmployeeNotFound.Error()},
				{ID: 1, Success: true},
			},
		},
		{
			name:     "TC-BULKSEND-03: Deactivated employee is not eligible and is reported without blocking the others",
			inputIDs: []int64{2, 1},
			setupMock: func(mockRepo *mocks.MockQuerier, mockMailer *mailermocks.MockClient) {
				mockRepo.EXPECT().GetEmployeeByID(gomock.Any(), int64(2)).Return(inactiveEmployee, nil)
				mockRepo.EXPECT().ListPositionIDsByEmployeeID(gomock.Any(), int64(2)).Return([]int64{}, nil)
				mockRepo.EXPECT().ListStoreIDsByEmployeeID(gomock.Any(), int64(2)).Return([]int64{}, nil)
				mockRepo.EXPECT().GetEmployeeByID(gomock.Any(), int64(1)).Return(activeEmployee, nil)
				mockRepo.EXPECT().ListPositionIDsByEmployeeID(gomock.Any(), int64(1)).Return([]int64{}, nil)
				mockRepo.EXPECT().ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).Return([]int64{}, nil)
				mockRepo.EXPECT().CreatePasswordResetToken(gomock.Any(), gomock.Any()).Return(repo.PasswordResetToken{ID: 1, EmployeeID: 1}, nil)
				mockMailer.EXPECT().Send(gomock.Any(), activeEmployee.Email, mailer.PasswordResetTemplate, gomock.Any()).Return(nil)
			},
			expected: []BulkActionResult{
				{ID: 2, Success: false, Error: ErrEmployeeNotActive.Error()},
				{ID: 1, Success: true},
			},
		},
		{
			name:     "TC-BULKSEND-04: Mailer failure for one id is reported without blocking the others",
			inputIDs: []int64{1},
			setupMock: func(mockRepo *mocks.MockQuerier, mockMailer *mailermocks.MockClient) {
				mockRepo.EXPECT().GetEmployeeByID(gomock.Any(), int64(1)).Return(activeEmployee, nil)
				mockRepo.EXPECT().ListPositionIDsByEmployeeID(gomock.Any(), int64(1)).Return([]int64{}, nil)
				mockRepo.EXPECT().ListStoreIDsByEmployeeID(gomock.Any(), int64(1)).Return([]int64{}, nil)
				mockRepo.EXPECT().CreatePasswordResetToken(gomock.Any(), gomock.Any()).Return(repo.PasswordResetToken{ID: 1, EmployeeID: 1}, nil)
				mockMailer.EXPECT().Send(gomock.Any(), activeEmployee.Email, mailer.PasswordResetTemplate, gomock.Any()).Return(dbErr)
			},
			expected: []BulkActionResult{{ID: 1, Success: false, Error: dbErr.Error()}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			mockMailer := mailermocks.NewMockClient(ctrl)
			mockOdoo := odoomocks.NewMockClient(ctrl)

			tc.setupMock(mockRepo, mockMailer)

			svc := newTestService(mockRepo, mockMailer, mockOdoo)

			results := svc.BulkSendPasswordResetLinks(ctx, tc.inputIDs)

			if len(results) != len(tc.expected) {
				t.Fatalf("expected %d results, got %d", len(tc.expected), len(results))
			}
			for i, want := range tc.expected {
				got := results[i]
				if got.ID != want.ID || got.Success != want.Success || got.Error != want.Error {
					t.Errorf("result[%d]: expected %+v, got %+v", i, want, got)
				}
			}
		})
	}
}

func TestEmployeeService_CompleteActivation(t *testing.T) {
	ctx := context.Background()

	validToken := repo.PasswordResetToken{ID: 5, EmployeeID: 1, TokenHash: tokenx.Hash("sometoken")}
	dbErr := errors.New("connection refused")

	tests := []struct {
		name        string
		inputParams completeActivationParams
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
	}{
		{
			name:        "TC-ACTIVATE-01: Completes activation successfully",
			inputParams: completeActivationParams{Token: "sometoken", Password: "supersecret"},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().RedeemPasswordResetToken(gomock.Any(), tokenx.Hash("sometoken")).Return(validToken, nil)
				mockRepo.EXPECT().
					SetEmployeePassword(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, arg repo.SetEmployeePasswordParams) (int64, error) {
						if arg.ID != validToken.EmployeeID {
							t.Errorf("expected SetEmployeePassword for employee %d, got %d", validToken.EmployeeID, arg.ID)
						}
						if err := bcrypt.CompareHashAndPassword(arg.Password, []byte("supersecret")); err != nil {
							t.Errorf("expected Password to be a bcrypt hash of the input password: %v", err)
						}
						return 1, nil
					})
			},
			expectedErr: nil,
		},
		{
			name:        "TC-ACTIVATE-02: Unknown/expired/used token translates to ErrInvalidOrExpiredToken",
			inputParams: completeActivationParams{Token: "badtoken", Password: "supersecret"},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().RedeemPasswordResetToken(gomock.Any(), tokenx.Hash("badtoken")).Return(repo.PasswordResetToken{}, pgx.ErrNoRows)
			},
			expectedErr: ErrInvalidOrExpiredToken,
		},
		{
			name:        "TC-ACTIVATE-03: Fails on database error redeeming the token",
			inputParams: completeActivationParams{Token: "sometoken", Password: "supersecret"},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().RedeemPasswordResetToken(gomock.Any(), tokenx.Hash("sometoken")).Return(repo.PasswordResetToken{}, dbErr)
			},
			expectedErr: dbErr,
		},
		{
			name:        "TC-ACTIVATE-04: Fails on database error setting the password",
			inputParams: completeActivationParams{Token: "sometoken", Password: "supersecret"},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().RedeemPasswordResetToken(gomock.Any(), tokenx.Hash("sometoken")).Return(validToken, nil)
				mockRepo.EXPECT().SetEmployeePassword(gomock.Any(), gomock.Any()).Return(int64(0), dbErr)
			},
			expectedErr: dbErr,
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

			err := svc.CompleteActivation(ctx, tc.inputParams)

			if tc.expectedErr != nil {
				if err == nil {
					t.Fatalf("Expected error '%v', but got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("Expected error '%v', but got '%v'", tc.expectedErr, err)
				}
			} else if err != nil {
				t.Fatalf("Expected no error, but got: %v", err)
			}
		})
	}
}

// TestEmployeeService_CreateEmployee_SendsActivationEmailAsynchronously
// verifies CreateEmployee returns as soon as the employee row is created,
// without waiting for the activation email to be sent (see the `go` call
// in service.go). A blocking mailer mock proves this deterministically:
// if CreateEmployee only returned after the mailer call completed, it would
// hang on the still-blocked Send below and fail the timeout.
func TestEmployeeService_CreateEmployee_SendsActivationEmailAsynchronously(t *testing.T) {
	ctx := context.Background()

	params := createEmployeeParams{
		OdooEmployeeID: 31,
		FullName:       "Tran Thi B",
		Email:          "tran-b@example.com",
		Username:       "tranthib",
	}
	repoParams := repo.CreateEmployeeParams{
		OdooEmployeeID: params.OdooEmployeeID,
		FullName:       params.FullName,
		Email:          params.Email,
		Username:       params.Username,
	}

	ctrl := gomock.NewController(t)
	mockRepo := mocks.NewMockQuerier(ctrl)
	mockMailer := mailermocks.NewMockClient(ctrl)
	mockOdoo := odoomocks.NewMockClient(ctrl)

	mockOdoo.EXPECT().
		FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{params.OdooEmployeeID}).
		Return([]odoo.Employee{{OdooEmployeeID: params.OdooEmployeeID}}, nil)
	mockRepo.EXPECT().
		CreateEmployee(gomock.Any(), repoParams).
		Return(repo.Employee{
			ID:             2,
			OdooEmployeeID: params.OdooEmployeeID,
			FullName:       params.FullName,
			Email:          params.Email,
			Username:       params.Username,
		}, nil)
	mockRepo.EXPECT().
		CreatePasswordResetToken(gomock.Any(), gomock.Any()).
		Return(repo.PasswordResetToken{ID: 2, EmployeeID: 2}, nil)

	sendStarted := make(chan struct{})
	release := make(chan struct{})
	sendFinished := make(chan struct{})
	mockMailer.EXPECT().
		Send(gomock.Any(), params.Email, mailer.AccountActivationTemplate, gomock.Any()).
		DoAndReturn(func(context.Context, string, string, any) error {
			close(sendStarted)
			<-release
			close(sendFinished)
			return nil
		})

	svc := newTestService(mockRepo, mockMailer, mockOdoo)

	returned := make(chan struct{})
	var detail EmployeeDetail
	var err error
	go func() {
		defer close(returned)
		detail, err = svc.CreateEmployee(ctx, params)
	}()

	select {
	case <-returned:
	case <-time.After(mailerWaitTimeout):
		t.Fatal("CreateEmployee did not return before the mailer send completed; it appears to be blocking on the mailer instead of running it in the background")
	}
	if err != nil {
		t.Fatalf("Expected no error, but got: %v", err)
	}
	if detail.Employee.ID == 0 {
		t.Error("Expected created employee to have a non-zero ID, but got 0")
	}

	select {
	case <-sendStarted:
	case <-time.After(mailerWaitTimeout):
		t.Fatal("timed out waiting for the background goroutine to invoke the mailer")
	}

	close(release)

	select {
	case <-sendFinished:
	case <-time.After(mailerWaitTimeout):
		t.Fatal("timed out waiting for the background mailer send to finish")
	}
}

// waitForSyncStatus polls SyncStatus until syncing matches want or
// mailerWaitTimeout elapses — unlock() runs asynchronously, right after a
// mocked call's DoAndReturn returns, in the background goroutine.
func waitForSyncStatus(t *testing.T, svc Service, want bool) {
	t.Helper()
	deadline := time.Now().Add(mailerWaitTimeout)
	for time.Now().Before(deadline) {
		if svc.SyncStatus(context.Background()).Syncing == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for syncing=%v", want)
}

func TestEmployeeService_SyncEmployees(t *testing.T) {
	ctx := context.Background()

	t.Run("TC-SYNC-01: returns quickly, upserts found ids, and reports not-found ids without failing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := mocks.NewMockQuerier(ctrl)
		mockMailer := mailermocks.NewMockClient(ctrl)
		mockOdoo := odoomocks.NewMockClient(ctrl)

		ids := []int64{1, 2, 3}
		odooEmployeeIDs := []int64{101, 102, 999}

		mockRepo.EXPECT().
			ListEmployeeIDsByIDs(gomock.Any(), ids).
			Return(odooEmployeeIDs, nil)

		fetchStarted := make(chan struct{})
		release := make(chan struct{})
		mockOdoo.EXPECT().
			FetchEmployeesByOdooEmployeeIDs(gomock.Any(), odooEmployeeIDs).
			DoAndReturn(func(context.Context, []int64) ([]odoo.Employee, error) {
				close(fetchStarted)
				<-release
				return []odoo.Employee{
					{OdooEmployeeID: 101, FullName: "Nguyen Van A", Email: "van-a@example.com"},
					{OdooEmployeeID: 102, FullName: "Tran Thi B", Email: "tran-b@example.com"},
				}, nil
			})

		upsertDone := make(chan struct{})
		mockRepo.EXPECT().
			UpsertEmployees(gomock.Any(), repo.UpsertEmployeesParams{
				OdooEmployeeIds: []int64{101, 102},
				FullNames:       []string{"Nguyen Van A", "Tran Thi B"},
				Emails:          []string{"van-a@example.com", "tran-b@example.com"},
			}).
			DoAndReturn(func(context.Context, repo.UpsertEmployeesParams) ([]repo.UpsertEmployeesRow, error) {
				defer close(upsertDone)
				return []repo.UpsertEmployeesRow{
					{ID: 1, OdooEmployeeID: 101, Inserted: true},
					{ID: 2, OdooEmployeeID: 102, Inserted: false},
				}, nil
			})
		// Neither found employee reports any Odoo store (StoreIDs unset),
		// but the diff still runs per employee with an empty set — clearing
		// any stale employee_stores rows rather than skipping the batch
		// entirely. InsertEmployeeStores still runs too (dbx.DiffReplace
		// always calls it) — the underlying sqlc INSERT...SELECT
		// unnest([])...ON CONFLICT DO NOTHING is already a safe no-op over
		// an empty set.
		mockRepo.EXPECT().
			DeleteEmployeeStoresNotIn(gomock.Any(), repo.DeleteEmployeeStoresNotInParams{
				EmployeeID: 1,
				StoreIds:   []int64{},
			}).
			Return(nil)
		mockRepo.EXPECT().
			InsertEmployeeStores(gomock.Any(), repo.InsertEmployeeStoresParams{
				EmployeeID: 1,
				StoreIds:   []int64{},
			}).
			Return(nil)
		mockRepo.EXPECT().
			DeleteEmployeeStoresNotIn(gomock.Any(), repo.DeleteEmployeeStoresNotInParams{
				EmployeeID: 2,
				StoreIds:   []int64{},
			}).
			Return(nil)
		mockRepo.EXPECT().
			InsertEmployeeStores(gomock.Any(), repo.InsertEmployeeStoresParams{
				EmployeeID: 2,
				StoreIds:   []int64{},
			}).
			Return(nil)

		svc := newTestService(mockRepo, mockMailer, mockOdoo)

		if svc.SyncStatus(ctx).Syncing {
			t.Fatal("expected syncing=false before SyncEmployees is called")
		}

		if err := svc.SyncEmployees(ctx, ids); err != nil {
			t.Fatalf("SyncEmployees() error = %v", err)
		}

		select {
		case <-fetchStarted:
		case <-time.After(mailerWaitTimeout):
			t.Fatal("SyncEmployees did not return before Odoo was fetched; it appears to be blocking instead of running in the background")
		}

		if !svc.SyncStatus(ctx).Syncing {
			t.Error("expected syncing=true while the background sync is in flight")
		}

		close(release)

		select {
		case <-upsertDone:
		case <-time.After(mailerWaitTimeout):
			t.Fatal("timed out waiting for the background upsert to run")
		}

		waitForSyncStatus(t, svc, false)
	})

	t.Run("TC-SYNC-05: resolves and diffs employee store membership from Odoo's x_pos_shop_ids, skipping an unresolvable store id", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := mocks.NewMockQuerier(ctrl)
		mockMailer := mailermocks.NewMockClient(ctrl)
		mockOdoo := odoomocks.NewMockClient(ctrl)

		mockRepo.EXPECT().
			ListEmployeeIDsByIDs(gomock.Any(), []int64{1}).
			Return([]int64{101}, nil)
		mockOdoo.EXPECT().
			FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{101}).
			Return([]odoo.Employee{
				{OdooEmployeeID: 101, FullName: "Nguyen Van A", Email: "van-a@example.com", StoreIDs: []int{10, 20, 999}},
			}, nil)
		mockRepo.EXPECT().
			UpsertEmployees(gomock.Any(), repo.UpsertEmployeesParams{
				OdooEmployeeIds: []int64{101},
				FullNames:       []string{"Nguyen Van A"},
				Emails:          []string{"van-a@example.com"},
			}).
			Return([]repo.UpsertEmployeesRow{{ID: 1, OdooEmployeeID: 101, Inserted: false}}, nil)
		mockRepo.EXPECT().
			ListStoresByOdooStoreIDs(gomock.Any(), []string{"10", "20", "999"}).
			Return([]repo.ListStoresByOdooStoreIDsRow{
				{ID: 501, OdooStoreID: pgtype.Text{String: "10", Valid: true}},
				{ID: 502, OdooStoreID: pgtype.Text{String: "20", Valid: true}},
				// 999 deliberately absent: not yet known locally.
			}, nil)
		deleteDone := make(chan struct{})
		mockRepo.EXPECT().
			DeleteEmployeeStoresNotIn(gomock.Any(), repo.DeleteEmployeeStoresNotInParams{
				EmployeeID: 1,
				StoreIds:   []int64{501, 502},
			}).
			Return(nil)
		mockRepo.EXPECT().
			InsertEmployeeStores(gomock.Any(), repo.InsertEmployeeStoresParams{
				EmployeeID: 1,
				StoreIds:   []int64{501, 502},
			}).
			DoAndReturn(func(context.Context, repo.InsertEmployeeStoresParams) error {
				defer close(deleteDone)
				return nil
			})

		svc := newTestService(mockRepo, mockMailer, mockOdoo)

		if err := svc.SyncEmployees(ctx, []int64{1}); err != nil {
			t.Fatalf("SyncEmployees() error = %v", err)
		}

		select {
		case <-deleteDone:
		case <-time.After(mailerWaitTimeout):
			t.Fatal("timed out waiting for the background store-membership sync to run")
		}

		waitForSyncStatus(t, svc, false)
	})

	t.Run("TC-SYNC-02: a second call while a sync is in flight is rejected with ErrSyncInProgress, and the lock is released after completion", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := mocks.NewMockQuerier(ctrl)
		mockMailer := mailermocks.NewMockClient(ctrl)
		mockOdoo := odoomocks.NewMockClient(ctrl)

		mockRepo.EXPECT().
			ListEmployeeIDsByIDs(gomock.Any(), []int64{1}).
			Return([]int64{101}, nil)
		mockRepo.EXPECT().
			ListEmployeeIDsByIDs(gomock.Any(), []int64{2}).
			Return([]int64{102}, nil)

		fetchStarted := make(chan struct{})
		release := make(chan struct{})
		mockOdoo.EXPECT().
			FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{101}).
			DoAndReturn(func(context.Context, []int64) ([]odoo.Employee, error) {
				close(fetchStarted)
				<-release
				return nil, nil
			})
		thirdCallDone := make(chan struct{})
		mockOdoo.EXPECT().
			FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{102}).
			DoAndReturn(func(context.Context, []int64) ([]odoo.Employee, error) {
				defer close(thirdCallDone)
				return nil, nil
			})

		svc := newTestService(mockRepo, mockMailer, mockOdoo)

		if err := svc.SyncEmployees(ctx, []int64{1}); err != nil {
			t.Fatalf("first SyncEmployees() error = %v", err)
		}

		select {
		case <-fetchStarted:
		case <-time.After(mailerWaitTimeout):
			t.Fatal("timed out waiting for the first sync to reach Odoo")
		}

		if err := svc.SyncEmployees(ctx, []int64{2}); !errors.Is(err, ErrSyncInProgress) {
			t.Errorf("second SyncEmployees() error = %v, want ErrSyncInProgress", err)
		}

		close(release)
		waitForSyncStatus(t, svc, false)

		if err := svc.SyncEmployees(ctx, []int64{2}); err != nil {
			t.Errorf("SyncEmployees() after completion, error = %v, want nil", err)
		}

		select {
		case <-thirdCallDone:
		case <-time.After(mailerWaitTimeout):
			t.Fatal("timed out waiting for the third sync to reach Odoo")
		}
		waitForSyncStatus(t, svc, false)
	})

	t.Run("TC-SYNC-03: an Odoo error is logged and releases the lock without upserting", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := mocks.NewMockQuerier(ctrl)
		mockMailer := mailermocks.NewMockClient(ctrl)
		mockOdoo := odoomocks.NewMockClient(ctrl)

		mockRepo.EXPECT().
			ListEmployeeIDsByIDs(gomock.Any(), []int64{1}).
			Return([]int64{101}, nil)

		done := make(chan struct{})
		mockOdoo.EXPECT().
			FetchEmployeesByOdooEmployeeIDs(gomock.Any(), []int64{101}).
			DoAndReturn(func(context.Context, []int64) ([]odoo.Employee, error) {
				defer close(done)
				return nil, errors.New("odoo: connection refused")
			})
		// No UpsertEmployees expectation: calling it would fail the mock
		// controller, asserting the upsert never runs after a fetch error.

		svc := newTestService(mockRepo, mockMailer, mockOdoo)

		if err := svc.SyncEmployees(ctx, []int64{1}); err != nil {
			t.Fatalf("SyncEmployees() error = %v", err)
		}

		select {
		case <-done:
		case <-time.After(mailerWaitTimeout):
			t.Fatal("timed out waiting for the background fetch to run")
		}

		waitForSyncStatus(t, svc, false)
	})

	t.Run("TC-SYNC-04: a ListEmployeeIDsByIDs error is returned synchronously and releases the lock without reaching Odoo", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := mocks.NewMockQuerier(ctrl)
		mockMailer := mailermocks.NewMockClient(ctrl)
		mockOdoo := odoomocks.NewMockClient(ctrl)

		mockRepo.EXPECT().
			ListEmployeeIDsByIDs(gomock.Any(), []int64{1}).
			Return(nil, errors.New("db: connection refused"))
		// No FetchEmployeesByOdooEmployeeIDs expectation: calling it would
		// fail the mock controller, asserting the sync never reaches Odoo
		// after a lookup error.

		svc := newTestService(mockRepo, mockMailer, mockOdoo)

		if err := svc.SyncEmployees(ctx, []int64{1}); err == nil {
			t.Fatal("expected SyncEmployees() to return the lookup error, got nil")
		}

		waitForSyncStatus(t, svc, false)
	})
}
