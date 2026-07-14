package stores

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	repo "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc"
	sqlcmocks "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/adapters/postgresql/sqlc/mocks"
	"github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo"
	odoomocks "github.com/Bang-Bien-Holding/bangnails.employee-app-backend/internal/odoo/mocks"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"
)

// newTestService builds a service whose withTx calls fn directly against q,
// bypassing real transaction plumbing — SyncStores' orchestration is
// exercised the same way regardless of what begins/commits the transaction.
// GetStoreByID doesn't need a transaction, so it reads through repo instead —
// set to the same mock so both styles of test can share one ctrl/mockRepo.
func newTestService(q repo.Querier, o odoo.Client) *service {
	return &service{
		repo: q,
		withTx: func(ctx context.Context, fn func(repo.Querier) error) error {
			return fn(q)
		},
		odoo: o,
	}
}

func TestStoreService_GetStoreByID(t *testing.T) {
	t.Run("returns the store detail with its wifi whitelist", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		want := repo.Store{
			ID:        12,
			StoreName: "Montpellier 1",
			IsActive:  true,
		}
		mockRepo.EXPECT().GetStoreByID(gomock.Any(), int64(12)).Return(want, nil)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return(
			[]netip.Addr{netip.MustParseAddr("138.101.10.1"), netip.MustParseAddr("138.101.10.2")}, nil,
		)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return(
			[]net.HardwareAddr{mustParseMAC(t, "aa:bb:cc:dd:ee:ff")}, nil,
		)

		svc := newTestService(mockRepo, nil)

		detail, err := svc.GetStoreByID(t.Context(), 12)
		if err != nil {
			t.Fatalf("GetStoreByID() error = %v", err)
		}
		if detail.Store != want {
			t.Errorf("GetStoreByID() store = %+v, want %+v", detail.Store, want)
		}
		wantIPs := []string{"138.101.10.1", "138.101.10.2"}
		if !equalStrings(detail.IPAddresses, wantIPs) {
			t.Errorf("GetStoreByID() ip_addresses = %v, want %v", detail.IPAddresses, wantIPs)
		}
		wantMACs := []string{"aa:bb:cc:dd:ee:ff"}
		if !equalStrings(detail.MACAddresses, wantMACs) {
			t.Errorf("GetStoreByID() mac_addresses = %v, want %v", detail.MACAddresses, wantMACs)
		}
	})

	t.Run("a store with no wifi entries returns empty, not nil, address lists", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().GetStoreByID(gomock.Any(), int64(12)).Return(repo.Store{ID: 12, IsActive: true}, nil)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return([]netip.Addr{}, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return([]net.HardwareAddr{}, nil)

		svc := newTestService(mockRepo, nil)

		detail, err := svc.GetStoreByID(t.Context(), 12)
		if err != nil {
			t.Fatalf("GetStoreByID() error = %v", err)
		}
		if detail.IPAddresses == nil || len(detail.IPAddresses) != 0 {
			t.Errorf("GetStoreByID() ip_addresses = %#v, want non-nil empty slice", detail.IPAddresses)
		}
		if detail.MACAddresses == nil || len(detail.MACAddresses) != 0 {
			t.Errorf("GetStoreByID() mac_addresses = %#v, want non-nil empty slice", detail.MACAddresses)
		}
	})

	t.Run("an unknown or inactive store id returns ErrStoreNotFound", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().GetStoreByID(gomock.Any(), int64(999)).Return(repo.Store{}, pgx.ErrNoRows)

		svc := newTestService(mockRepo, nil)

		if _, err := svc.GetStoreByID(t.Context(), 999); !errors.Is(err, ErrStoreNotFound) {
			t.Errorf("GetStoreByID() error = %v, want ErrStoreNotFound", err)
		}
	})
}

func mustParseMAC(t *testing.T, s string) net.HardwareAddr {
	t.Helper()
	mac, err := net.ParseMAC(s)
	if err != nil {
		t.Fatalf("net.ParseMAC(%q) error = %v", s, err)
	}
	return mac
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestStoreService_SyncStores(t *testing.T) {
	t.Run("fetches all stores in a single call and reports insert/update counts", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockOdoo.EXPECT().FetchStores(gomock.Any()).Return([]odoo.Store{
			{ID: 1, Name: "A", City: "Hanoi"},
			{ID: 2, Name: "B", City: "Ho Chi Minh City"},
		}, nil)

		mockRepo.EXPECT().UpsertStores(gomock.Any(), repo.UpsertStoresParams{
			OdooStoreIds: []string{"1", "2"},
			StoreNames:   []string{"A", "B"},
			Cities:       []string{"Hanoi", "Ho Chi Minh City"},
		}).Return([]repo.UpsertStoresRow{
			{ID: 10, OdooStoreID: pgtype.Text{String: "1", Valid: true}, Inserted: true},
			{ID: 20, OdooStoreID: pgtype.Text{String: "2", Valid: true}, Inserted: false},
		}, nil)

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

	t.Run("soft-deletes stores odoo no longer reports", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockOdoo.EXPECT().FetchStores(gomock.Any()).Return([]odoo.Store{
			{ID: 1, Name: "A", City: "Hanoi"},
		}, nil)

		mockRepo.EXPECT().UpsertStores(gomock.Any(), repo.UpsertStoresParams{
			OdooStoreIds: []string{"1"},
			StoreNames:   []string{"A"},
			Cities:       []string{"Hanoi"},
		}).Return([]repo.UpsertStoresRow{
			{ID: 10, OdooStoreID: pgtype.Text{String: "1", Valid: true}, Inserted: false},
		}, nil)

		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{"1"}).Return([]int64{5, 6}, nil)
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

	t.Run("empty store list from odoo soft-deletes every previously active store", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockOdoo.EXPECT().FetchStores(gomock.Any()).Return([]odoo.Store{}, nil)

		mockRepo.EXPECT().UpsertStores(gomock.Any(), repo.UpsertStoresParams{
			OdooStoreIds: []string{},
			StoreNames:   []string{},
			Cities:       []string{},
		}).Return([]repo.UpsertStoresRow{}, nil)

		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{}).Return([]int64{5, 6, 7}, nil)
		mockRepo.EXPECT().SoftDeleteStores(gomock.Any(), []int64{5, 6, 7}).Return(int64(3), nil)

		svc := newTestService(mockRepo, mockOdoo)

		summary, err := svc.SyncStores(t.Context())
		if err != nil {
			t.Fatalf("SyncStores() error = %v", err)
		}
		if summary.DeletedStores != 3 {
			t.Errorf("summary.DeletedStores = %d, want 3", summary.DeletedStores)
		}
	})

	t.Run("a second call while a sync is in flight is rejected with ErrSyncInProgress", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		started := make(chan struct{})
		release := make(chan struct{})
		mockOdoo.EXPECT().FetchStores(gomock.Any()).DoAndReturn(
			func(ctx context.Context) ([]odoo.Store, error) {
				close(started)
				<-release
				return []odoo.Store{}, nil
			},
		)
		mockRepo.EXPECT().UpsertStores(gomock.Any(), gomock.Any()).Return([]repo.UpsertStoresRow{}, nil)
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
		mockOdoo.EXPECT().FetchStores(gomock.Any()).Return([]odoo.Store{}, nil)
		mockRepo.EXPECT().UpsertStores(gomock.Any(), gomock.Any()).Return([]repo.UpsertStoresRow{}, nil)
		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{}).Return([]int64{}, nil)
		if _, err := svc.SyncStores(t.Context()); err != nil {
			t.Errorf("SyncStores() after lock release, error = %v, want nil", err)
		}
	})

	t.Run("the lock is released even when fetching from odoo fails", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		boom := errors.New("boom")
		gomock.InOrder(
			mockOdoo.EXPECT().FetchStores(gomock.Any()).Return(nil, boom),
			mockOdoo.EXPECT().FetchStores(gomock.Any()).Return([]odoo.Store{}, nil),
		)
		mockRepo.EXPECT().UpsertStores(gomock.Any(), gomock.Any()).Return([]repo.UpsertStoresRow{}, nil)
		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{}).Return([]int64{}, nil)

		svc := newTestService(mockRepo, mockOdoo)

		if _, err := svc.SyncStores(t.Context()); !errors.Is(err, boom) {
			t.Fatalf("SyncStores() error = %v, want %v", err, boom)
		}

		if _, err := svc.SyncStores(t.Context()); err != nil {
			t.Errorf("SyncStores() after earlier failure, error = %v, want nil", err)
		}
	})

	t.Run("the lock is released even when the store transaction fails", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		boom := errors.New("boom")
		gomock.InOrder(
			mockOdoo.EXPECT().FetchStores(gomock.Any()).Return([]odoo.Store{
				{ID: 1, Name: "A", City: "Hanoi"},
			}, nil),
			mockOdoo.EXPECT().FetchStores(gomock.Any()).Return([]odoo.Store{}, nil),
		)
		mockRepo.EXPECT().UpsertStores(gomock.Any(), repo.UpsertStoresParams{
			OdooStoreIds: []string{"1"},
			StoreNames:   []string{"A"},
			Cities:       []string{"Hanoi"},
		}).Return(nil, boom)
		mockRepo.EXPECT().UpsertStores(gomock.Any(), repo.UpsertStoresParams{
			OdooStoreIds: []string{},
			StoreNames:   []string{},
			Cities:       []string{},
		}).Return([]repo.UpsertStoresRow{}, nil)
		// FindStoresNotInOdoo must NOT be called on the first (failed) call —
		// only one expectation is set, satisfied by the second, successful
		// retry, so gomock fails the test if the service calls it after the
		// failed upsert too.
		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{}).Return([]int64{}, nil)

		svc := newTestService(mockRepo, mockOdoo)

		summary, err := svc.SyncStores(t.Context())
		if !errors.Is(err, boom) {
			t.Fatalf("SyncStores() error = %v, want %v", err, boom)
		}
		if summary != (SyncSummary{}) {
			t.Errorf("SyncStores() summary = %+v, want zero value", summary)
		}

		if _, err := svc.SyncStores(t.Context()); err != nil {
			t.Errorf("SyncStores() after earlier failure, error = %v, want nil", err)
		}
	})

	t.Run("a failure after a successful upsert still returns a zero-value summary", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockOdoo := odoomocks.NewMockClient(ctrl)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		boom := errors.New("boom")
		mockOdoo.EXPECT().FetchStores(gomock.Any()).Return([]odoo.Store{
			{ID: 1, Name: "A", City: "Hanoi"},
		}, nil)
		mockRepo.EXPECT().UpsertStores(gomock.Any(), gomock.Any()).Return([]repo.UpsertStoresRow{
			{ID: 10, OdooStoreID: pgtype.Text{String: "1", Valid: true}, Inserted: true},
		}, nil)
		mockRepo.EXPECT().FindStoresNotInOdoo(gomock.Any(), []string{"1"}).Return(nil, boom)

		svc := newTestService(mockRepo, mockOdoo)

		summary, err := svc.SyncStores(t.Context())
		if !errors.Is(err, boom) {
			t.Fatalf("SyncStores() error = %v, want %v", err, boom)
		}
		if summary != (SyncSummary{}) {
			t.Errorf("SyncStores() summary = %+v, want zero value even though the upsert succeeded", summary)
		}
	})
}
