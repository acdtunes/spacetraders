package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// fakeShipyardYardsProvider is the driving-port test double for
// runShipyardYards — it stands in for *persistence.ShipyardInventoryRepositoryGORM
// so these tests exercise CLI rendering/wiring only; era-scoping and price
// ordering are real behavior asserted against a real DB in
// shipyard_inventory_repository_test.go (Mandate 4: adapters get integration
// tests, not mocks).
type fakeShipyardYardsProvider struct {
	rows []shipyard.ShipTypeAvailability
	err  error
}

func (f *fakeShipyardYardsProvider) ListSavedYards(_ context.Context, _ int, _ []string) ([]shipyard.ShipTypeAvailability, error) {
	return f.rows, f.err
}

func TestRunShipyardYards_RendersRowsInGivenOrder(t *testing.T) {
	scanned := time.Date(2026, 7, 19, 8, 30, 0, 0, time.UTC)
	f := &fakeShipyardYardsProvider{rows: []shipyard.ShipTypeAvailability{
		{SystemSymbol: "X1-BB", WaypointSymbol: "X1-BB-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 1_150_000, Supply: "HIGH", LastScanned: scanned},
		{SystemSymbol: "X1-AA", WaypointSymbol: "X1-AA-Y1", ShipType: "SHIP_HEAVY_FREIGHTER", PurchasePrice: 1_300_000, Supply: "MODERATE", LastScanned: scanned},
	}}
	var out bytes.Buffer

	err := runShipyardYards(context.Background(), f, &out, 4, []string{"SHIP_HEAVY_FREIGHTER"})

	require.NoError(t, err)
	rendered := out.String()
	require.Regexp(t, `SYSTEM\s+WAYPOINT\s+TYPE\s+PRICE\s+SUPPLY\s+LAST SCANNED`, rendered, "tabwriter pads columns with spaces, not literal tabs, once flushed")
	cheapAt := bytes.Index(out.Bytes(), []byte("X1-BB-Y1"))
	pricierAt := bytes.Index(out.Bytes(), []byte("X1-AA-Y1"))
	require.True(t, cheapAt >= 0 && pricierAt > cheapAt, "rows must render in the order the provider returns them (price-ascending is the repository's job)")
	require.Contains(t, rendered, "1150000")
	require.Contains(t, rendered, "2026-07-19")
}

func TestRunShipyardYards_NoRowsPrintsFriendlyMessage(t *testing.T) {
	f := &fakeShipyardYardsProvider{rows: nil}
	var out bytes.Buffer

	err := runShipyardYards(context.Background(), f, &out, 4, []string{"SHIP_HEAVY_FREIGHTER"})

	require.NoError(t, err)
	require.Contains(t, out.String(), "No saved yards found")
}

func TestRunShipyardYards_PropagatesProviderError(t *testing.T) {
	f := &fakeShipyardYardsProvider{err: errors.New("db unreachable")}
	var out bytes.Buffer

	err := runShipyardYards(context.Background(), f, &out, 4, []string{"SHIP_HEAVY_FREIGHTER"})

	require.Error(t, err, "a read failure must surface, never be silently swallowed")
	require.Contains(t, err.Error(), "db unreachable")
}
