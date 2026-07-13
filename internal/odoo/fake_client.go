package odoo

import (
	"context"
	"fmt"
)

// totalFakeStores is how many records FakeClient pretends Odoo holds.
const totalFakeStores = 12

var fakeCityNames = []string{
	"Hanoi", "Ho Chi Minh City", "Da Nang", "Hai Phong", "Can Tho",
	"Nha Trang", "Hue", "Vung Tau", "Bien Hoa", "Quy Nhon",
}

// FakeClient is a deterministic, in-memory stand-in for a real Odoo
// connection (Phase 2 of the store-sync spec: no live Odoo integration
// exists yet). It always reports the same totalFakeStores records, so
// tests and the sync flow behave identically across runs.
type FakeClient struct{}

func NewFakeClient() *FakeClient {
	return &FakeClient{}
}

func (c *FakeClient) FetchStores(ctx context.Context) ([]Store, error) {
	stores := make([]Store, 0, totalFakeStores)
	for i := range totalFakeStores {
		id := i + 1
		stores = append(stores, Store{
			ID:   id,
			Name: fmt.Sprintf("Store #%d", id),
			City: fakeCityNames[id%len(fakeCityNames)],
		})
	}
	return stores, nil
}
