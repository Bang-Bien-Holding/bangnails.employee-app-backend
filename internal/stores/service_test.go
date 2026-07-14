package stores

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

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

	t.Run("an unknown store id returns ErrStoreNotFound", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().GetStoreByID(gomock.Any(), int64(999)).Return(repo.Store{}, pgx.ErrNoRows)

		svc := newTestService(mockRepo, nil)

		if _, err := svc.GetStoreByID(t.Context(), 999); !errors.Is(err, ErrStoreNotFound) {
			t.Errorf("GetStoreByID() error = %v, want ErrStoreNotFound", err)
		}
	})

	t.Run("an inactive store is found, not ErrStoreNotFound", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().GetStoreByID(gomock.Any(), int64(12)).Return(repo.Store{ID: 12, IsActive: false}, nil)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return([]netip.Addr{}, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return([]net.HardwareAddr{}, nil)

		svc := newTestService(mockRepo, nil)

		detail, err := svc.GetStoreByID(t.Context(), 12)
		if err != nil {
			t.Fatalf("GetStoreByID() error = %v, want nil for an inactive store", err)
		}
		if detail.Store.IsActive {
			t.Errorf("GetStoreByID() store.IsActive = true, want false")
		}
	})
}

func TestStoreService_ListStores(t *testing.T) {
	t.Run("returns every store, including inactive ones, in the query's order", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().ListStores(gomock.Any()).Return([]repo.ListStoresRow{
			{
				Store:        repo.Store{ID: 10, StoreName: "Hanoi 1", City: pgtype.Text{String: "Hanoi", Valid: true}, IsActive: true},
				IpAddresses:  []netip.Addr{netip.MustParseAddr("138.101.10.1")},
				MacAddresses: []net.HardwareAddr{},
			},
			{
				Store:        repo.Store{ID: 20, StoreName: "Montpellier 1", City: pgtype.Text{String: "Montpellier", Valid: true}, IsActive: false},
				IpAddresses:  []netip.Addr{},
				MacAddresses: []net.HardwareAddr{mustParseMAC(t, "aa:bb:cc:dd:ee:ff")},
			},
		}, nil)

		svc := newTestService(mockRepo, nil)

		details, err := svc.ListStores(t.Context())
		if err != nil {
			t.Fatalf("ListStores() error = %v", err)
		}
		if len(details) != 2 {
			t.Fatalf("ListStores() returned %d stores, want 2", len(details))
		}
		if details[0].Store.ID != 10 || details[0].Store.IsActive != true {
			t.Errorf("details[0] = %+v, want active store id 10", details[0].Store)
		}
		wantIPs := []string{"138.101.10.1"}
		if !equalStrings(details[0].IPAddresses, wantIPs) {
			t.Errorf("details[0].IPAddresses = %v, want %v", details[0].IPAddresses, wantIPs)
		}
		if details[1].Store.ID != 20 || details[1].Store.IsActive != false {
			t.Errorf("details[1] = %+v, want inactive store id 20", details[1].Store)
		}
		wantMACs := []string{"aa:bb:cc:dd:ee:ff"}
		if !equalStrings(details[1].MACAddresses, wantMACs) {
			t.Errorf("details[1].MACAddresses = %v, want %v", details[1].MACAddresses, wantMACs)
		}
	})

	t.Run("an empty store table returns a non-nil empty slice", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().ListStores(gomock.Any()).Return([]repo.ListStoresRow{}, nil)

		svc := newTestService(mockRepo, nil)

		details, err := svc.ListStores(t.Context())
		if err != nil {
			t.Fatalf("ListStores() error = %v", err)
		}
		if details == nil || len(details) != 0 {
			t.Errorf("ListStores() = %#v, want non-nil empty slice", details)
		}
	})

	t.Run("a repo error is propagated", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		boom := errors.New("boom")
		mockRepo.EXPECT().ListStores(gomock.Any()).Return(nil, boom)

		svc := newTestService(mockRepo, nil)

		if _, err := svc.ListStores(t.Context()); !errors.Is(err, boom) {
			t.Errorf("ListStores() error = %v, want %v", err, boom)
		}
	})
}

func TestStoreService_UpdateStore(t *testing.T) {
	t.Run("successfully updates the geofence and returns the updated detail", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		updatedAt := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
		lat, lon := 1.1, 100.2
		radius := int32(50)

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, arg repo.UpdateStoreGeofenceParams) (repo.Store, error) {
				if arg.ID != 12 {
					t.Errorf("UpdateStoreGeofenceParams.ID = %d, want 12", arg.ID)
				}
				gotLat, _ := arg.Latitude.Float64Value()
				if !arg.Latitude.Valid || gotLat.Float64 != lat {
					t.Errorf("UpdateStoreGeofenceParams.Latitude = %+v, want valid %v", arg.Latitude, lat)
				}
				gotLon, _ := arg.Longitude.Float64Value()
				if !arg.Longitude.Valid || gotLon.Float64 != lon {
					t.Errorf("UpdateStoreGeofenceParams.Longitude = %+v, want valid %v", arg.Longitude, lon)
				}
				if !arg.RadiusMeters.Valid || arg.RadiusMeters.Int32 != radius {
					t.Errorf("UpdateStoreGeofenceParams.RadiusMeters = %+v, want valid %d", arg.RadiusMeters, radius)
				}
				if !arg.ExpectedUpdatedAt.Valid || !arg.ExpectedUpdatedAt.Time.Equal(updatedAt) {
					t.Errorf("UpdateStoreGeofenceParams.ExpectedUpdatedAt = %+v, want %v", arg.ExpectedUpdatedAt, updatedAt)
				}
				return repo.Store{ID: 12, StoreName: "Montpellier 1", IsActive: true}, nil
			},
		)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return([]netip.Addr{}, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return([]net.HardwareAddr{}, nil)

		svc := newTestService(mockRepo, nil)

		detail, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{
			UpdatedAt:    updatedAt,
			Latitude:     &lat,
			Longitude:    &lon,
			RadiusMeters: &radius,
		})
		if err != nil {
			t.Fatalf("UpdateStore() error = %v", err)
		}
		if detail.Store.ID != 12 {
			t.Errorf("UpdateStore() store.ID = %d, want 12", detail.Store.ID)
		}
	})

	t.Run("a request with no geofence fields still bumps updated_at and leaves the geofence untouched", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		updatedAt := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, arg repo.UpdateStoreGeofenceParams) (repo.Store, error) {
				if arg.Latitude.Valid || arg.Longitude.Valid || arg.RadiusMeters.Valid {
					t.Errorf("UpdateStoreGeofenceParams = %+v, want all geofence columns invalid/NULL", arg)
				}
				return repo.Store{ID: 12, IsActive: true}, nil
			},
		)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return([]netip.Addr{}, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return([]net.HardwareAddr{}, nil)

		svc := newTestService(mockRepo, nil)

		if _, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{UpdatedAt: updatedAt}); err != nil {
			t.Fatalf("UpdateStore() error = %v", err)
		}
	})

	t.Run("a stale updated_at is rejected with ErrStoreConflict", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).Return(repo.Store{}, pgx.ErrNoRows)
		mockRepo.EXPECT().GetStoreByID(gomock.Any(), int64(12)).Return(repo.Store{ID: 12, IsActive: true}, nil)

		svc := newTestService(mockRepo, nil)

		if _, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{UpdatedAt: time.Now()}); !errors.Is(err, ErrStoreConflict) {
			t.Errorf("UpdateStore() error = %v, want ErrStoreConflict", err)
		}
	})

	t.Run("an unknown store id returns ErrStoreNotFound", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).Return(repo.Store{}, pgx.ErrNoRows)
		mockRepo.EXPECT().GetStoreByID(gomock.Any(), int64(999)).Return(repo.Store{}, pgx.ErrNoRows)

		svc := newTestService(mockRepo, nil)

		if _, err := svc.UpdateStore(t.Context(), 999, patchStoreParams{UpdatedAt: time.Now()}); !errors.Is(err, ErrStoreNotFound) {
			t.Errorf("UpdateStore() error = %v, want ErrStoreNotFound", err)
		}
	})

	t.Run("omitting ip_addresses and mac_addresses leaves both whitelists untouched", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).Return(repo.Store{ID: 12, IsActive: true}, nil)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return([]netip.Addr{}, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return([]net.HardwareAddr{}, nil)
		// No EXPECT for Delete/InsertStoreWifi{IPs,Macs} — gomock fails the
		// test if UpdateStore calls any of them when both lists are nil.

		svc := newTestService(mockRepo, nil)

		if _, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{UpdatedAt: time.Now()}); err != nil {
			t.Fatalf("UpdateStore() error = %v", err)
		}
	})

	t.Run("submitting ip_addresses replaces the IP whitelist, independent of mac_addresses", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		wantIPs := []netip.Addr{netip.MustParseAddr("138.101.10.1"), netip.MustParseAddr("138.101.10.2")}

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).Return(repo.Store{ID: 12, IsActive: true}, nil)
		mockRepo.EXPECT().DeleteStoreWifiIPsNotIn(gomock.Any(), repo.DeleteStoreWifiIPsNotInParams{
			StoreID: 12, IpAddresses: wantIPs,
		}).Return(nil)
		mockRepo.EXPECT().InsertStoreWifiIPs(gomock.Any(), repo.InsertStoreWifiIPsParams{
			StoreID: 12, IpAddresses: wantIPs,
		}).Return(nil)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return(wantIPs, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return([]net.HardwareAddr{}, nil)
		// No EXPECT for Delete/InsertStoreWifiMacs — mac_addresses is nil.

		svc := newTestService(mockRepo, nil)

		ips := []string{"138.101.10.1", "138.101.10.2"}
		detail, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{UpdatedAt: time.Now(), IPAddresses: ips})
		if err != nil {
			t.Fatalf("UpdateStore() error = %v", err)
		}
		wantStrs := []string{"138.101.10.1", "138.101.10.2"}
		if !equalStrings(detail.IPAddresses, wantStrs) {
			t.Errorf("UpdateStore() ip_addresses = %v, want %v", detail.IPAddresses, wantStrs)
		}
	})

	t.Run("submitting an empty ip_addresses array clears the whitelist", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).Return(repo.Store{ID: 12, IsActive: true}, nil)
		mockRepo.EXPECT().DeleteStoreWifiIPsNotIn(gomock.Any(), repo.DeleteStoreWifiIPsNotInParams{
			StoreID: 12, IpAddresses: []netip.Addr{},
		}).Return(nil)
		mockRepo.EXPECT().InsertStoreWifiIPs(gomock.Any(), repo.InsertStoreWifiIPsParams{
			StoreID: 12, IpAddresses: []netip.Addr{},
		}).Return(nil)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return([]netip.Addr{}, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return([]net.HardwareAddr{}, nil)

		svc := newTestService(mockRepo, nil)

		empty := []string{}
		if _, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{UpdatedAt: time.Now(), IPAddresses: empty}); err != nil {
			t.Fatalf("UpdateStore() error = %v", err)
		}
	})

	t.Run("submitting mac_addresses replaces the MAC whitelist, independent of ip_addresses", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		wantMACs := []net.HardwareAddr{mustParseMAC(t, "aa:bb:cc:dd:ee:ff")}

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).Return(repo.Store{ID: 12, IsActive: true}, nil)
		mockRepo.EXPECT().DeleteStoreWifiMacsNotIn(gomock.Any(), repo.DeleteStoreWifiMacsNotInParams{
			StoreID: 12, MacAddresses: wantMACs,
		}).Return(nil)
		mockRepo.EXPECT().InsertStoreWifiMacs(gomock.Any(), repo.InsertStoreWifiMacsParams{
			StoreID: 12, MacAddresses: wantMACs,
		}).Return(nil)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return([]netip.Addr{}, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return(wantMACs, nil)

		svc := newTestService(mockRepo, nil)

		macs := []string{"aa:bb:cc:dd:ee:ff"}
		if _, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{UpdatedAt: time.Now(), MACAddresses: macs}); err != nil {
			t.Fatalf("UpdateStore() error = %v", err)
		}
	})

	t.Run("a wifi-only PATCH still enforces the updated_at conflict check before touching any wifi table", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).Return(repo.Store{}, pgx.ErrNoRows)
		mockRepo.EXPECT().GetStoreByID(gomock.Any(), int64(12)).Return(repo.Store{ID: 12, IsActive: true}, nil)
		// No EXPECT for Delete/InsertStoreWifiIPs — the conflict must be
		// caught before the wifi whitelist is ever touched.

		svc := newTestService(mockRepo, nil)

		ips := []string{"138.101.10.1"}
		if _, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{UpdatedAt: time.Now(), IPAddresses: ips}); !errors.Is(err, ErrStoreConflict) {
			t.Errorf("UpdateStore() error = %v, want ErrStoreConflict", err)
		}
	})

	t.Run("is_active: true reactivates a currently-inactive store", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, arg repo.UpdateStoreGeofenceParams) (repo.Store, error) {
				if !arg.IsActive.Valid || !arg.IsActive.Bool {
					t.Errorf("UpdateStoreGeofenceParams.IsActive = %+v, want valid true", arg.IsActive)
				}
				return repo.Store{ID: 12, IsActive: true}, nil
			},
		)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return([]netip.Addr{}, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return([]net.HardwareAddr{}, nil)

		svc := newTestService(mockRepo, nil)

		isActive := true
		detail, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{UpdatedAt: time.Now(), IsActive: &isActive})
		if err != nil {
			t.Fatalf("UpdateStore() error = %v", err)
		}
		if !detail.Store.IsActive {
			t.Errorf("UpdateStore() store.IsActive = false, want true")
		}
	})

	t.Run("is_active: false deactivates a store without touching geofence or wifi lists", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, arg repo.UpdateStoreGeofenceParams) (repo.Store, error) {
				if arg.Latitude.Valid || arg.Longitude.Valid || arg.RadiusMeters.Valid {
					t.Errorf("UpdateStoreGeofenceParams = %+v, want geofence columns invalid/NULL", arg)
				}
				if !arg.IsActive.Valid || arg.IsActive.Bool {
					t.Errorf("UpdateStoreGeofenceParams.IsActive = %+v, want valid false", arg.IsActive)
				}
				return repo.Store{ID: 12, IsActive: false}, nil
			},
		)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return([]netip.Addr{}, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return([]net.HardwareAddr{}, nil)
		// No EXPECT for Delete/InsertStoreWifi{IPs,Macs} — both lists omitted.

		svc := newTestService(mockRepo, nil)

		isActive := false
		detail, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{UpdatedAt: time.Now(), IsActive: &isActive})
		if err != nil {
			t.Fatalf("UpdateStore() error = %v", err)
		}
		if detail.Store.IsActive {
			t.Errorf("UpdateStore() store.IsActive = true, want false")
		}
	})

	t.Run("omitting is_active leaves the store's active state untouched", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockRepo := sqlcmocks.NewMockQuerier(ctrl)

		mockRepo.EXPECT().UpdateStoreGeofence(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, arg repo.UpdateStoreGeofenceParams) (repo.Store, error) {
				if arg.IsActive.Valid {
					t.Errorf("UpdateStoreGeofenceParams.IsActive = %+v, want invalid/NULL", arg.IsActive)
				}
				return repo.Store{ID: 12, IsActive: true}, nil
			},
		)
		mockRepo.EXPECT().ListStoreWifiIPsByStoreID(gomock.Any(), int64(12)).Return([]netip.Addr{}, nil)
		mockRepo.EXPECT().ListStoreWifiMacsByStoreID(gomock.Any(), int64(12)).Return([]net.HardwareAddr{}, nil)

		svc := newTestService(mockRepo, nil)

		if _, err := svc.UpdateStore(t.Context(), 12, patchStoreParams{UpdatedAt: time.Now()}); err != nil {
			t.Fatalf("UpdateStore() error = %v", err)
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
