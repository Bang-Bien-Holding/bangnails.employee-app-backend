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

			svc := NewService(mockRepo)
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

			svc := NewService(mockRepo)
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

			svc := NewService(mockRepo)
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

			svc := NewService(mockRepo)
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
