package parkedsensing

// The sensing quote's listing memo is a SECOND writer into shipyard_inventory, reached without
// passing the scanner's own persist check — so the read-mode discriminator has to hold here too.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	shipyardDomain "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// memoInventory captures what the quote memo persisted, which is the only observable the write has.
type memoInventory struct {
	shipyardDomain.InventoryRepository
	rows []shipyardDomain.ShipTypeAvailability
}

func (m *memoInventory) ReplaceScan(_ context.Context, _ int, _, _ string, rows []shipyardDomain.ShipTypeAvailability, _ time.Time) error {
	m.rows = append(m.rows, rows...)
	return nil
}

// A quote runs where a probe of ours already stands, so its priced listings arrive with a supply
// tier. Dropping that on the way to the store writes a priced row nothing can attribute to a
// presence read, which is exactly the shape the store-wide invariant refuses.
func TestProbeQuoteMemo_PersistsSupplyBesideEveryAsk(t *testing.T) {
	store := &memoInventory{}
	port := NewProbePurchasePort(nil, nil, store)

	port.persistListings(context.Background(), 1, "X1-AA-Y1", shipyardDomain.NewShipyard(
		"X1-AA-Y1",
		[]string{"SHIP_PROBE", "SHIP_HEAVY_FREIGHTER"},
		[]shipyardDomain.ShipListing{{ShipType: "SHIP_PROBE", PurchasePrice: 25_000, Supply: "ABUNDANT"}},
		0,
	), time.Now())

	require.Len(t, store.rows, 2, "the whole catalogue is memoed, priced or not")
	require.Empty(t, shipyardDomain.DisagreeingRows(store.rows),
		"a memoed ask must carry the supply it was quoted with, and an unpriced row must carry none")
}
