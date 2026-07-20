package positions

import (
	"context"
	"errors"
	"testing"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc/mocks"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/mock/gomock"
)

// newTestService builds a service whose withTx calls fn directly against q,
// bypassing real transaction plumbing (mirrors stores/service_test.go).
func newTestService(q repo.Querier) *service {
	return &service{
		repo: q,
		withTx: func(ctx context.Context, fn func(repo.Querier) error) error {
			return fn(q)
		},
	}
}

func TestPositionService_CreatePosition(t *testing.T) {
	ctx := context.Background()

	dbErr := errors.New("connection refused")
	pgDupNameErr := &pgconn.PgError{
		Code:           uniqueViolationCode,
		ConstraintName: positionsNameKeyConstraint,
		Message:        `duplicate key value violates unique constraint "positions_name_key"`,
	}

	tests := []struct {
		name        string
		inputParams createPositionParams
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
	}{
		{
			name:        "TC-POST-01: Create position successfully",
			inputParams: createPositionParams{Name: "Technician"},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreatePosition(gomock.Any(), "Technician").
					Return(repo.Position{ID: 1, Name: "Technician"}, nil)
			},
			expectedErr: nil,
		},
		{
			name:        "TC-POST-02: Translates a Postgres unique-violation on name to ErrPositionNameAlreadyExists",
			inputParams: createPositionParams{Name: "Technician"},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreatePosition(gomock.Any(), "Technician").
					Return(repo.Position{}, pgDupNameErr)
			},
			expectedErr: ErrPositionNameAlreadyExists,
		},
		{
			name:        "TC-POST-03: Create position fails on database error",
			inputParams: createPositionParams{Name: "Technician"},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					CreatePosition(gomock.Any(), "Technician").
					Return(repo.Position{}, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			tc.setupMock(mockRepo)

			svc := newTestService(mockRepo)
			position, err := svc.CreatePosition(ctx, tc.inputParams)

			if tc.expectedErr != nil {
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("expected error %v, got %v", tc.expectedErr, err)
				}
				if position.ID != 0 {
					t.Error("expected zero-value position response when an error occurs")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if position.ID == 0 {
				t.Error("expected created position to have a non-zero ID")
			}
		})
	}
}

func TestPositionService_ListPositions(t *testing.T) {
	ctx := context.Background()
	dbErr := errors.New("connection refused")

	tests := []struct {
		name        string
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
		expectedLen int
	}{
		{
			name: "TC-LIST-01: List positions successfully",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					ListPositions(gomock.Any()).
					Return([]repo.Position{{ID: 1, Name: "Manager"}, {ID: 2, Name: "Technician"}}, nil)
			},
			expectedLen: 2,
		},
		{
			name: "TC-LIST-02: List positions returns an empty slice when there are none",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().ListPositions(gomock.Any()).Return([]repo.Position{}, nil)
			},
			expectedLen: 0,
		},
		{
			name: "TC-LIST-03: List positions fails on database error",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().ListPositions(gomock.Any()).Return(nil, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			tc.setupMock(mockRepo)

			svc := newTestService(mockRepo)
			got, err := svc.ListPositions(ctx)

			if tc.expectedErr != nil {
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("expected error %v, got %v", tc.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if len(got) != tc.expectedLen {
				t.Errorf("expected %d positions, got %d", tc.expectedLen, len(got))
			}
		})
	}
}

func TestPositionService_UpdatePosition(t *testing.T) {
	ctx := context.Background()
	dbErr := errors.New("connection refused")
	pgDupNameErr := &pgconn.PgError{
		Code:           uniqueViolationCode,
		ConstraintName: positionsNameKeyConstraint,
		Message:        `duplicate key value violates unique constraint "positions_name_key"`,
	}

	tests := []struct {
		name        string
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
	}{
		{
			name: "TC-PUT-01: Rename a position successfully",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdatePosition(gomock.Any(), repo.UpdatePositionParams{ID: 1, Name: "Senior Technician"}).
					Return(repo.Position{ID: 1, Name: "Senior Technician"}, nil)
			},
		},
		{
			name: "TC-PUT-02: Translates a no-rows repo error to ErrPositionNotFound",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdatePosition(gomock.Any(), repo.UpdatePositionParams{ID: 1, Name: "Senior Technician"}).
					Return(repo.Position{}, pgx.ErrNoRows)
			},
			expectedErr: ErrPositionNotFound,
		},
		{
			name: "TC-PUT-03: Translates a Postgres unique-violation on name to ErrPositionNameAlreadyExists",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdatePosition(gomock.Any(), repo.UpdatePositionParams{ID: 1, Name: "Senior Technician"}).
					Return(repo.Position{}, pgDupNameErr)
			},
			expectedErr: ErrPositionNameAlreadyExists,
		},
		{
			name: "TC-PUT-04: Update position fails on database error",
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().
					UpdatePosition(gomock.Any(), repo.UpdatePositionParams{ID: 1, Name: "Senior Technician"}).
					Return(repo.Position{}, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			tc.setupMock(mockRepo)

			svc := newTestService(mockRepo)
			_, err := svc.UpdatePosition(ctx, 1, updatePositionParams{Name: "Senior Technician"})

			if tc.expectedErr != nil {
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("expected error %v, got %v", tc.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

func TestPositionService_GetPositionEmployees(t *testing.T) {
	ctx := context.Background()
	dbErr := errors.New("connection refused")

	tests := []struct {
		name        string
		inputID     int64
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
		expectedIDs []int64
	}{
		{
			name:    "TC-GET-01: List employees assigned to an existing position",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().GetPositionByID(gomock.Any(), int64(1)).Return(repo.Position{ID: 1, Name: "Technician"}, nil)
				mockRepo.EXPECT().ListEmployeeIDsByPositionID(gomock.Any(), int64(1)).Return([]int64{10, 20}, nil)
			},
			expectedIDs: []int64{10, 20},
		},
		{
			name:    "TC-GET-02: Unknown position id translates no-rows to ErrPositionNotFound",
			inputID: 999,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().GetPositionByID(gomock.Any(), int64(999)).Return(repo.Position{}, pgx.ErrNoRows)
			},
			expectedErr: ErrPositionNotFound,
		},
		{
			name:    "TC-GET-03: Fails on database error fetching the position",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().GetPositionByID(gomock.Any(), int64(1)).Return(repo.Position{}, dbErr)
			},
			expectedErr: dbErr,
		},
		{
			name:    "TC-GET-04: Fails on database error listing employee ids",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().GetPositionByID(gomock.Any(), int64(1)).Return(repo.Position{ID: 1, Name: "Technician"}, nil)
				mockRepo.EXPECT().ListEmployeeIDsByPositionID(gomock.Any(), int64(1)).Return(nil, dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			tc.setupMock(mockRepo)

			svc := newTestService(mockRepo)
			got, err := svc.GetPositionEmployees(ctx, tc.inputID)

			if tc.expectedErr != nil {
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("expected error %v, got %v", tc.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if len(got) != len(tc.expectedIDs) {
				t.Fatalf("expected %v, got %v", tc.expectedIDs, got)
			}
			for i, id := range tc.expectedIDs {
				if got[i] != id {
					t.Errorf("expected %v, got %v", tc.expectedIDs, got)
				}
			}
		})
	}
}

func TestPositionService_SetPositionEmployees(t *testing.T) {
	ctx := context.Background()
	dbErr := errors.New("connection refused")

	tests := []struct {
		name        string
		inputID     int64
		inputParams setPositionEmployeesParams
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
		expectedIDs []int64
	}{
		{
			name:        "TC-SET-01: Replaces the position's employee set",
			inputID:     1,
			inputParams: setPositionEmployeesParams{EmployeeIDs: []int64{10, 20}},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().GetPositionByID(gomock.Any(), int64(1)).Return(repo.Position{ID: 1, Name: "Technician"}, nil)
				mockRepo.EXPECT().CountEmployeesByIDs(gomock.Any(), []int64{10, 20}).Return(int64(2), nil)
				mockRepo.EXPECT().DeleteEmployeePositionsByPositionIDNotIn(gomock.Any(), repo.DeleteEmployeePositionsByPositionIDNotInParams{
					PositionID:  1,
					EmployeeIds: []int64{10, 20},
				}).Return(nil)
				mockRepo.EXPECT().InsertPositionEmployees(gomock.Any(), repo.InsertPositionEmployeesParams{
					PositionID:  1,
					EmployeeIds: []int64{10, 20},
				}).Return(nil)
			},
			expectedIDs: []int64{10, 20},
		},
		{
			name:        "TC-SET-02: Empty set clears every assignment without inserting",
			inputID:     1,
			inputParams: setPositionEmployeesParams{EmployeeIDs: nil},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().GetPositionByID(gomock.Any(), int64(1)).Return(repo.Position{ID: 1, Name: "Technician"}, nil)
				mockRepo.EXPECT().DeleteEmployeePositionsByPositionIDNotIn(gomock.Any(), repo.DeleteEmployeePositionsByPositionIDNotInParams{
					PositionID:  1,
					EmployeeIds: []int64{},
				}).Return(nil)
			},
			expectedIDs: []int64{},
		},
		{
			name:        "TC-SET-03: Unknown position id translates no-rows to ErrPositionNotFound",
			inputID:     999,
			inputParams: setPositionEmployeesParams{EmployeeIDs: []int64{10}},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().GetPositionByID(gomock.Any(), int64(999)).Return(repo.Position{}, pgx.ErrNoRows)
			},
			expectedErr: ErrPositionNotFound,
		},
		{
			name:        "TC-SET-04: An id not referencing a real employee translates to ErrUnknownEmployeeID",
			inputID:     1,
			inputParams: setPositionEmployeesParams{EmployeeIDs: []int64{10, 999}},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().GetPositionByID(gomock.Any(), int64(1)).Return(repo.Position{ID: 1, Name: "Technician"}, nil)
				mockRepo.EXPECT().CountEmployeesByIDs(gomock.Any(), []int64{10, 999}).Return(int64(1), nil)
			},
			expectedErr: ErrUnknownEmployeeID,
		},
		{
			name:        "TC-SET-05: Fails on database error deleting stale assignments",
			inputID:     1,
			inputParams: setPositionEmployeesParams{EmployeeIDs: []int64{10}},
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().GetPositionByID(gomock.Any(), int64(1)).Return(repo.Position{ID: 1, Name: "Technician"}, nil)
				mockRepo.EXPECT().CountEmployeesByIDs(gomock.Any(), []int64{10}).Return(int64(1), nil)
				mockRepo.EXPECT().DeleteEmployeePositionsByPositionIDNotIn(gomock.Any(), repo.DeleteEmployeePositionsByPositionIDNotInParams{
					PositionID:  1,
					EmployeeIds: []int64{10},
				}).Return(dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			tc.setupMock(mockRepo)

			svc := newTestService(mockRepo)
			got, err := svc.SetPositionEmployees(ctx, tc.inputID, tc.inputParams)

			if tc.expectedErr != nil {
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("expected error %v, got %v", tc.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if len(got) != len(tc.expectedIDs) {
				t.Fatalf("expected %v, got %v", tc.expectedIDs, got)
			}
			for i, id := range tc.expectedIDs {
				if got[i] != id {
					t.Errorf("expected %v, got %v", tc.expectedIDs, got)
				}
			}
		})
	}
}

func TestPositionService_DeletePosition(t *testing.T) {
	ctx := context.Background()
	dbErr := errors.New("connection refused")

	tests := []struct {
		name        string
		inputID     int64
		setupMock   func(mockRepo *mocks.MockQuerier)
		expectedErr error
	}{
		{
			name:    "TC-DEL-01: Delete an existing position",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().DeletePosition(gomock.Any(), int64(1)).Return(int64(1), nil)
			},
		},
		{
			name:    "TC-DEL-02: Unknown id translates zero rows affected to ErrPositionNotFound",
			inputID: 999,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().DeletePosition(gomock.Any(), int64(999)).Return(int64(0), nil)
			},
			expectedErr: ErrPositionNotFound,
		},
		{
			name:    "TC-DEL-03: Fails on database error",
			inputID: 1,
			setupMock: func(mockRepo *mocks.MockQuerier) {
				mockRepo.EXPECT().DeletePosition(gomock.Any(), int64(1)).Return(int64(0), dbErr)
			},
			expectedErr: dbErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockRepo := mocks.NewMockQuerier(ctrl)
			tc.setupMock(mockRepo)

			svc := newTestService(mockRepo)
			err := svc.DeletePosition(ctx, tc.inputID)

			if tc.expectedErr != nil {
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("expected error %v, got %v", tc.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
