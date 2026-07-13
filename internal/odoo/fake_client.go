package odoo

import (
	"context"
	"fmt"
)

// totalFakeStores is how many records FakeClient pretends Odoo holds.
const totalFakeStores = 250

var fakeCityNames = []string{
	"Hanoi", "Ho Chi Minh City", "Da Nang", "Hai Phong", "Can Tho",
	"Nha Trang", "Hue", "Vung Tau", "Bien Hoa", "Quy Nhon",
}

// fakeUserPool is the set of Odoo user IDs FakeClient draws store
// assignments from — small and shared across stores on purpose, so
// exercising the sync's "clear assignments no longer in Odoo" /
// "assign users newly in Odoo" logic actually moves the same users
// between stores across pages instead of every store getting a disjoint set.
var fakeUserPool = []int{1, 3, 5, 7, 9, 12, 18, 23, 30, 41}

// FakeClient is a deterministic, in-memory stand-in for a real Odoo
// connection (Phase 2 of the store-sync spec: no live Odoo integration
// exists yet). It always reports the same totalFakeStores records for a
// given offset, so tests and the sync loop behave identically across runs.
type FakeClient struct{}

func NewFakeClient() *FakeClient {
	return &FakeClient{}
}

func (c *FakeClient) FetchStores(ctx context.Context, limit, offset int) ([]Store, error) {
	if offset >= totalFakeStores {
		return []Store{}, nil
	}

	end := min(offset+limit, totalFakeStores)

	stores := make([]Store, 0, end-offset)
	for i := offset; i < end; i++ {
		id := i + 1
		stores = append(stores, Store{
			ID:          id,
			Name:        fmt.Sprintf("Store #%d", id),
			City:        fakeCityNames[id%len(fakeCityNames)],
			OdooUserIDs: fakeUserIDsFor(id),
		})
	}
	return stores, nil
}

// fakeUserIDsFor deterministically assigns 1-3 users from fakeUserPool to
// store id, using id itself to pick the starting point and count so the
// same id always yields the same assignment.
func fakeUserIDsFor(id int) []int {
	count := id%3 + 1
	start := id % len(fakeUserPool)

	ids := make([]int, 0, count)
	for i := range count {
		ids = append(ids, fakeUserPool[(start+i)%len(fakeUserPool)])
	}
	return ids
}
