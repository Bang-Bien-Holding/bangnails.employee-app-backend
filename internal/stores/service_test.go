package stores

import (
	"context"
	"errors"
	"testing"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	sqlcmocks "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc/mocks"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo"
	odoomocks "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo/mocks"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"
)

// newTestService builds a service whose withTx calls fn directly against q,
// bypassing real transaction plumbing — SyncStores' orchestration is
// exercised the same way regardless of what begins/commits the transaction.
func newTestService(q repo.Querier, o odoo.Client) *service {
	return &service{
		withTx: func(ctx context.Context, fn func(repo.Querier) error) error {
			return fn(q)
		},
		odoo: o,
	}
}

func TestStoreService_SyncStores(t *testing.T) {
	t.Run("upserts a batch, syncs assignments, and reports insert/update counts", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockOdoo.EXPECT().FetchStores(gomock.Any(), 100, 0).Return([]odoo.Store{
			{ID: 1, Name: "A", City: "Hanoi", OdooUserIDs: []int{101, 102}},
			{ID: 2, Name: "B", City: "Ho Chi Minh City", OdooUserIDs: []int{}},
		}, nil)
		mockOdoo.EXPECT().FetchStores(gomock.Any(), 100, 100).Return([]odoo.Store{}, nil)

		mockRepo.EXPECT().UpsertStores(gomock.Any(), repo.UpsertStoresParams{
			OdooStoreIds: []string{"1", "2"},
			StoreNames:   []string{"A", "B"},
			Cities:       []string{"Hanoi", "Ho Chi Minh City"},
		}).Return([]repo.UpsertStoresRow{
			{ID: 10, OdooStoreID: pgtype.Text{String: "1", Valid: true}, Inserted: true},
			{ID: 20, OdooStoreID: pgtype.Text{String: "2", Valid: true}, Inserted: false},
		}, nil)

		mockRepo.EXPECT().ClearStoreAssignmentsNotInOdoo(gomock.Any(), repo.ClearStoreAssignmentsNotInOdooParams{
			StoreID:         pgtype.Int8{Int64: 10, Valid: true},
			KeepEmployeeIds: []string{"101", "102"},
		}).Return(nil)
		mockRepo.EXPECT().AssignEmployeesToStore(gomock.Any(), repo.AssignEmployeesToStoreParams{
			StoreID:           pgtype.Int8{Int64: 10, Valid: true},
			AssignEmployeeIds: []string{"101", "102"},
		}).Return(nil)

		mockRepo.EXPECT().ClearStoreAssignmentsNotInOdoo(gomock.Any(), repo.ClearStoreAssignmentsNotInOdooParams{
			StoreID:         pgtype.Int8{Int64: 20, Valid: true},
			KeepEmployeeIds: []string{},
		}).Return(nil)
		mockRepo.EXPECT().AssignEmployeesToStore(gomock.Any(), repo.AssignEmployeesToStoreParams{
			StoreID:           pgtype.Int8{Int64: 20, Valid: true},
			AssignEmployeeIds: []string{},
		}).Return(nil)

		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{"1", "2"}).Return([]int64{}, nil)

		svc := newTestService(mockRepo, mockOdoo)

		summary, err := svc.SyncStores(t.Context())
		if err != nil {
			t.Fatalf("SyncStores() error = %v", err)
		}

		want := SyncSummary{TotalStoresProcessed: 2, InsertedStores: 1, UpdatedStores: 1}
		if summary != want {
			t.Errorf("SyncStores() summary = %+v, want %+v", summary, want)
		}
	})

	t.Run("soft-deletes stores no longer reported by odoo after the fetch loop ends", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockOdoo.EXPECT().FetchStores(gomock.Any(), 100, 0).Return([]odoo.Store{}, nil)

		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{}).Return([]int64{5, 6}, nil)
		mockRepo.EXPECT().ClearEmployeeAssignmentsForStores(gomock.Any(), []int64{5, 6}).Return(nil)
		mockRepo.EXPECT().SoftDeleteStores(gomock.Any(), []int64{5, 6}).Return(int64(2), nil)

		svc := newTestService(mockRepo, mockOdoo)

		summary, err := svc.SyncStores(t.Context())
		if err != nil {
			t.Fatalf("SyncStores() error = %v", err)
		}
		if summary.DeletedStores != 2 {
			t.Errorf("summary.DeletedStores = %d, want 2", summary.DeletedStores)
		}
	})

	t.Run("a second call while a sync is in flight is rejected with ErrSyncInProgress", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		started := make(chan struct{})
		release := make(chan struct{})
		mockOdoo.EXPECT().FetchStores(gomock.Any(), 100, 0).DoAndReturn(
			func(ctx context.Context, limit, offset int) ([]odoo.Store, error) {
				close(started)
				<-release
				return []odoo.Store{}, nil
			},
		)
		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{}).Return([]int64{}, nil)

		svc := newTestService(mockRepo, mockOdoo)

		done := make(chan error, 1)
		go func() {
			_, err := svc.SyncStores(t.Context())
			done <- err
		}()

		<-started

		if _, err := svc.SyncStores(t.Context()); !errors.Is(err, ErrSyncInProgress) {
			t.Errorf("second SyncStores() error = %v, want ErrSyncInProgress", err)
		}

		close(release)
		if err := <-done; err != nil {
			t.Fatalf("first SyncStores() error = %v", err)
		}

		// The lock must be released once the in-flight call finishes.
		mockOdoo.EXPECT().FetchStores(gomock.Any(), 100, 0).Return([]odoo.Store{}, nil)
		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{}).Return([]int64{}, nil)
		if _, err := svc.SyncStores(t.Context()); err != nil {
			t.Errorf("SyncStores() after lock release, error = %v, want nil", err)
		}
	})

	t.Run("the lock is released even when a batch fails", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		boom := errors.New("boom")
		gomock.InOrder(
			mockOdoo.EXPECT().FetchStores(gomock.Any(), 100, 0).Return(nil, boom),
			mockOdoo.EXPECT().FetchStores(gomock.Any(), 100, 0).Return([]odoo.Store{}, nil),
		)
		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{}).Return([]int64{}, nil)

		svc := newTestService(mockRepo, mockOdoo)

		if _, err := svc.SyncStores(t.Context()); !errors.Is(err, boom) {
			t.Fatalf("SyncStores() error = %v, want %v", err, boom)
		}

		if _, err := svc.SyncStores(t.Context()); err != nil {
			t.Errorf("SyncStores() after earlier failure, error = %v, want nil", err)
		}
	})
}
