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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/mock/gomock"
	"golang.org/x/crypto/bcrypt"
)

// mailerWaitTimeout bounds how long a test waits for the background
// sendActivationEmail goroutine (see the `go` call in service.go) to reach
// the mailer, so a stuck goroutine fails the test instead of hanging it.
const mailerWaitTimeout = time.Second

func TestEmployeeService_CreateEmployee(t *testing.T) {
	ctx := context.Background()

	// Default valid parameters for testing
	defaultParams := createEmployeeParams{
		EmployeeID: "M30",
		FullName:   "Nguyen Van A",
		Email:      "van-a@example.com",
		Username:   "nguyenvana",
		Role:       "technician",
	}

	defaultRepoParams := repo.CreateEmployeeParams{
		EmployeeID: defaultParams.EmployeeID,
		FullName:   defaultParams.FullName,
		Email:      defaultParams.Email,
		Username:   defaultParams.Username,
		Role:       defaultParams.Role,
	}

	// service.CreateEmployee only translates a *pgconn.PgError unique-violation
	// (code 23505) on a known constraint into a sentinel error; every other
	// error — including a plain error with matching text — passes through
	// unchanged. See translateEmployeeUniqueViolation in service.go.
	dupEmailErr := errors.New(`duplicate key value violates unique constraint "employees_email_key"`)
	dupEmployeeIDErr := errors.New(`duplicate key value violates unique constraint "employees_employee_id_key"`)
	dbErr := errors.New("connection refused")

	pgDupEmailErr := &pgconn.PgError{
		Code:           uniqueViolationCode,
		ConstraintName: employeesEmailKeyConstraint,
		Message:        `duplicate key value violates unique constraint "employees_email_key"`,
	}
	pgDupEmployeeIDErr := &pgconn.PgError{
		Code:           uniqueViolationCode,
		ConstraintName: employeesEmployeeIDKeyConstraint,
		Message:        `duplicate key value violates unique constraint "employees_employee_id_key"`,
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
		expectedErr     error
		checkResponse   func(t *testing.T, emp repo.Employee)
	}{
		{
			name:        "TC-POST-01: Create employee successfully",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{
						ID:         1,
						EmployeeID: defaultParams.EmployeeID,
						FullName:   defaultParams.FullName,
						Email:      defaultParams.Email,
						Username:   defaultParams.Username,
						Role:       defaultParams.Role,
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
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID == 0 {
					t.Error("Expected created employee to have a non-zero ID, but got 0")
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
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID != 0 {
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
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID != 0 {
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
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID != 0 {
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
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID != 0 {
					t.Error("Expected zero-value employee response when an error occurs")
				}
			},
		},
		{
			name:        "TC-POST-06: Translates a Postgres unique-violation on employee ID to ErrEmployeeIDAlreadyExists",
			inputParams: defaultParams,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreateEmployee(gomock.Any(), defaultRepoParams).
					Return(repo.Employee{}, pgDupEmployeeIDErr)
			},
			expectedErr: ErrEmployeeIDAlreadyExists,
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID != 0 {
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
						ID:         1,
						EmployeeID: defaultParams.EmployeeID,
						FullName:   defaultParams.FullName,
						Email:      defaultParams.Email,
						Username:   defaultParams.Username,
						Role:       defaultParams.Role,
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
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID == 0 {
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
						ID:         1,
						EmployeeID: defaultParams.EmployeeID,
						FullName:   defaultParams.FullName,
						Email:      defaultParams.Email,
						Username:   defaultParams.Username,
						Role:       defaultParams.Role,
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
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID == 0 {
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
						ID:         1,
						EmployeeID: defaultParams.EmployeeID,
						FullName:   defaultParams.FullName,
						Email:      defaultParams.Email,
						Username:   defaultParams.Username,
						Role:       defaultParams.Role,
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
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID == 0 {
					t.Error("Expected created employee to have a non-zero ID, but got 0")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			mockMailer := mailermocks.NewMockClient(ctrl)

			tc.setupMock(mockRepo)
			var mailerDone <-chan struct{}
			if tc.setupMailerMock != nil {
				mailerDone = tc.setupMailerMock(mockMailer)
			}

			svc := NewService(mockRepo, mockMailer)

			// Execute
			emp, err := svc.CreateEmployee(ctx, tc.inputParams)

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
				tc.checkResponse(t, emp)
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
		checkResponse func(t *testing.T, employees []repo.Employee)
	}{
		{
			name: "TC-LIST-01: List employees successfully",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					ListEmployees(gomock.Any()).
					Return([]repo.Employee{
						{ID: 1, EmployeeID: "M30", FullName: "Nguyen Van A"},
						{ID: 2, EmployeeID: "M31", FullName: "Tran Thi B"},
					}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, employees []repo.Employee) {
				if len(employees) != 2 {
					t.Fatalf("expected 2 employees, got %d", len(employees))
				}
			},
		},
		{
			name: "TC-LIST-02: List employees returns an empty slice when there are none",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					ListEmployees(gomock.Any()).
					Return([]repo.Employee{}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, employees []repo.Employee) {
				if len(employees) != 0 {
					t.Fatalf("expected 0 employees, got %d", len(employees))
				}
			},
		},
		{
			name: "TC-LIST-03: List employees fails on database error",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					ListEmployees(gomock.Any()).
					Return(nil, dbErr)
			},
			expectedErr: dbErr,
			checkResponse: func(t *testing.T, employees []repo.Employee) {
				if employees != nil {
					t.Errorf("expected nil employees when an error occurs, got %v", employees)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			mockMailer := mailermocks.NewMockClient(ctrl)

			tc.setupMock(mockRepo)

			svc := NewService(mockRepo, mockMailer)

			employees, err := svc.ListEmployees(ctx)

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
				tc.checkResponse(t, employees)
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
		checkResponse func(t *testing.T, emp repo.Employee)
	}{
		{
			name:    "TC-GET-01: Get employee by ID successfully",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					GetEmployeeByID(gomock.Any(), int64(1)).
					Return(repo.Employee{ID: 1, EmployeeID: "M30", FullName: "Nguyen Van A"}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID != 1 {
					t.Errorf("expected employee ID 1, got %d", emp.ID)
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
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID != 0 {
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
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.ID != 0 {
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

			tc.setupMock(mockRepo)

			svc := NewService(mockRepo, mockMailer)

			emp, err := svc.GetEmployeeByID(ctx, tc.inputID)

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
				tc.checkResponse(t, emp)
			}
		})
	}
}

func TestEmployeeService_UpdateEmployee(t *testing.T) {
	ctx := context.Background()

	inputParams := updateEmployeeParams{
		EmployeeID: "EMP-001",
		FullName:   "Nguyen Van A",
		Email:      "van-a@example.com",
		Username:   "nguyenvana",
		Role:       "technician",
	}
	repoParams := repo.UpdateEmployeeParams{
		ID:         1,
		EmployeeID: inputParams.EmployeeID,
		FullName:   inputParams.FullName,
		Email:      inputParams.Email,
		Username:   inputParams.Username,
		Role:       inputParams.Role,
	}

	dupEmailErr := &pgconn.PgError{
		Code:           uniqueViolationCode,
		ConstraintName: employeesEmailKeyConstraint,
		Message:        `duplicate key value violates unique constraint "employees_email_key"`,
	}
	dupUsernameErr := &pgconn.PgError{
		Code:           uniqueViolationCode,
		ConstraintName: employeesUsernameKeyConstraint,
		Message:        `duplicate key value violates unique constraint "employees_username_key"`,
	}
	dupEmployeeIDErr := &pgconn.PgError{
		Code:           uniqueViolationCode,
		ConstraintName: employeesEmployeeIDKeyConstraint,
		Message:        `duplicate key value violates unique constraint "employees_employee_id_key"`,
	}
	dbErr := errors.New("connection refused")

	tests := []struct {
		name          string
		setupMock     func(mockRepo *mocks.MockQuerier)
		expectedErr   error
		checkResponse func(t *testing.T, emp repo.Employee)
	}{
		{
			name: "TC-PUT-01: Update employee successfully",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{
						ID:       1,
						FullName: inputParams.FullName,
						Email:    inputParams.Email,
						Username: inputParams.Username,
						Role:     inputParams.Role,
					}, nil)
			},
			expectedErr: nil,
			checkResponse: func(t *testing.T, emp repo.Employee) {
				if emp.FullName != inputParams.FullName {
					t.Errorf("expected full name %q, got %q", inputParams.FullName, emp.FullName)
				}
			},
		},
		{
			name: "TC-PUT-02: Translates a no-rows repo error to ErrEmployeeNotFound",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{}, pgx.ErrNoRows)
			},
			expectedErr: ErrEmployeeNotFound,
		},
		{
			name: "TC-PUT-03: Translates a Postgres unique-violation on email to ErrEmailAlreadyExists",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{}, dupEmailErr)
			},
			expectedErr: ErrEmailAlreadyExists,
		},
		{
			name: "TC-PUT-04: Translates a Postgres unique-violation on username to ErrUsernameAlreadyExists",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{}, dupUsernameErr)
			},
			expectedErr: ErrUsernameAlreadyExists,
		},
		{
			name: "TC-PUT-04b: Translates a Postgres unique-violation on employee_id to ErrEmployeeIDAlreadyExists",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{}, dupEmployeeIDErr)
			},
			expectedErr: ErrEmployeeIDAlreadyExists,
		},
		{
			name: "TC-PUT-05: Update employee fails on database error",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{}, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			mockMailer := mailermocks.NewMockClient(ctrl)

			tc.setupMock(mockRepo)

			svc := NewService(mockRepo, mockMailer)

			emp, err := svc.UpdateEmployee(ctx, 1, inputParams)

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
				tc.checkResponse(t, emp)
			}
		})
	}
}

func TestEmployeeService_UpdateEmployee_Password(t *testing.T) {
	ctx := context.Background()

	baseParams := updateEmployeeParams{
		EmployeeID: "EMP-001",
		FullName:   "Nguyen Van A",
		Email:      "van-a@example.com",
		Username:   "nguyenvana",
		Role:       "technician",
	}
	repoParams := repo.UpdateEmployeeParams{
		ID:         1,
		EmployeeID: baseParams.EmployeeID,
		FullName:   baseParams.FullName,
		Email:      baseParams.Email,
		Username:   baseParams.Username,
		Role:       baseParams.Role,
	}
	dbErr := errors.New("connection refused")

	tests := []struct {
		name        string
		params      updateEmployeeParams
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
	}{
		{
			name: "TC-PUT-06: Nil password leaves the password column untouched",
			params: func() updateEmployeeParams {
				p := baseParams
				p.Password = nil
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{ID: 1}, nil)
				mockRepo.EXPECT().SetEmployeePassword(gomock.Any(), gomock.Any()).Times(0)
			},
			expectedErr: nil,
		},
		{
			name: "TC-PUT-07: Non-nil password bcrypt-hashes and sets it via SetEmployeePassword",
			params: func() updateEmployeeParams {
				p := baseParams
				pw := "supersecret"
				p.Password = &pw
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{ID: 1}, nil)
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
			name: "TC-PUT-08: Propagates an error from SetEmployeePassword",
			params: func() updateEmployeeParams {
				p := baseParams
				pw := "supersecret"
				p.Password = &pw
				return p
			}(),
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdateEmployee(gomock.Any(), repoParams).
					Return(repo.Employee{ID: 1}, nil)
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

			tc.setupMock(mockRepo)

			svc := NewService(mockRepo, mockMailer)

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

			tc.setupMock(mockRepo)

			svc := NewService(mockRepo, mockMailer)

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

			tc.setupMock(mockRepo)

			svc := NewService(mockRepo, mockMailer)

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

			tc.setupMock(mockRepo)

			svc := NewService(mockRepo, mockMailer)

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

			tc.setupMock(mockRepo)

			svc := NewService(mockRepo, mockMailer)

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
				mockRepo.EXPECT().GetEmployeeByID(gomock.Any(), int64(1)).Return(activeEmployee, nil)
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

			tc.setupMock(mockRepo, mockMailer)

			svc := NewService(mockRepo, mockMailer)

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

	validToken := repo.PasswordResetToken{ID: 5, EmployeeID: 1, TokenHash: hashToken("sometoken")}
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
				mockRepo.EXPECT().RedeemPasswordResetToken(gomock.Any(), hashToken("sometoken")).Return(validToken, nil)
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
				mockRepo.EXPECT().RedeemPasswordResetToken(gomock.Any(), hashToken("badtoken")).Return(repo.PasswordResetToken{}, pgx.ErrNoRows)
			},
			expectedErr: ErrInvalidOrExpiredToken,
		},
		{
			name:        "TC-ACTIVATE-03: Fails on database error redeeming the token",
			inputParams: completeActivationParams{Token: "sometoken", Password: "supersecret"},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().RedeemPasswordResetToken(gomock.Any(), hashToken("sometoken")).Return(repo.PasswordResetToken{}, dbErr)
			},
			expectedErr: dbErr,
		},
		{
			name:        "TC-ACTIVATE-04: Fails on database error setting the password",
			inputParams: completeActivationParams{Token: "sometoken", Password: "supersecret"},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().RedeemPasswordResetToken(gomock.Any(), hashToken("sometoken")).Return(validToken, nil)
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

			tc.setupMock(mockRepo)

			svc := NewService(mockRepo, mockMailer)

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
		EmployeeID: "M31",
		FullName:   "Tran Thi B",
		Email:      "tran-b@example.com",
		Username:   "tranthib",
		Role:       "technician",
	}
	repoParams := repo.CreateEmployeeParams{
		EmployeeID: params.EmployeeID,
		FullName:   params.FullName,
		Email:      params.Email,
		Username:   params.Username,
		Role:       params.Role,
	}

	ctrl := gomock.NewController(t)
	mockRepo := mocks.NewMockQuerier(ctrl)
	mockMailer := mailermocks.NewMockClient(ctrl)

	mockRepo.EXPECT().
		CreateEmployee(gomock.Any(), repoParams).
		Return(repo.Employee{
			ID:         2,
			EmployeeID: params.EmployeeID,
			FullName:   params.FullName,
			Email:      params.Email,
			Username:   params.Username,
			Role:       params.Role,
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

	svc := NewService(mockRepo, mockMailer)

	returned := make(chan struct{})
	var emp repo.Employee
	var err error
	go func() {
		defer close(returned)
		emp, err = svc.CreateEmployee(ctx, params)
	}()

	select {
	case <-returned:
	case <-time.After(mailerWaitTimeout):
		t.Fatal("CreateEmployee did not return before the mailer send completed; it appears to be blocking on the mailer instead of running it in the background")
	}
	if err != nil {
		t.Fatalf("Expected no error, but got: %v", err)
	}
	if emp.ID == 0 {
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
