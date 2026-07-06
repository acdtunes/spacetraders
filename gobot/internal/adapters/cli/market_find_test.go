package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	fn()

	require.NoError(t, w.Close())
	os.Stdout = original

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

type fakeMarketGoodFinder struct {
	listings []persistence.MarketGoodListing
	err      error
}

func (f *fakeMarketGoodFinder) FindMarketsTradingGood(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) ([]persistence.MarketGoodListing, error) {
	return f.listings, f.err
}

func waypointOrder(listings []persistence.MarketGoodListing) []string {
	out := make([]string, len(listings))
	for i, l := range listings {
		out[i] = l.WaypointSymbol
	}
	return out
}

func TestSortMarketListingsBySide(t *testing.T) {
	base := []persistence.MarketGoodListing{
		{WaypointSymbol: "W-MID", SellPrice: 50, PurchasePrice: 90},
		{WaypointSymbol: "W-CHEAP", SellPrice: 10, PurchasePrice: 20},
		{WaypointSymbol: "W-EXPENSIVE", SellPrice: 90, PurchasePrice: 60},
	}

	cases := []struct {
		side string
		want []string
	}{
		{"buy", []string{"W-CHEAP", "W-MID", "W-EXPENSIVE"}},
		{"any", []string{"W-CHEAP", "W-MID", "W-EXPENSIVE"}},
		{"sell", []string{"W-MID", "W-EXPENSIVE", "W-CHEAP"}},
	}

	for _, tc := range cases {
		t.Run(tc.side, func(t *testing.T) {
			listings := append([]persistence.MarketGoodListing(nil), base...)
			sortMarketListings(listings, tc.side)
			require.Equal(t, tc.want, waypointOrder(listings))
		})
	}
}

func TestRunMarketFindPropagatesRepositoryError(t *testing.T) {
	finder := &fakeMarketGoodFinder{err: errors.New("db unreachable")}
	err := runMarketFind(context.Background(), finder, "IRON_ORE", "", "buy", 1, false)
	require.Error(t, err)
}

func TestRunMarketFindHandlesNoResults(t *testing.T) {
	finder := &fakeMarketGoodFinder{}
	err := runMarketFind(context.Background(), finder, "IRON_ORE", "X1-TEST", "buy", 1, false)
	require.NoError(t, err)
}

func TestRunMarketFindReturnsResultsWithoutHidingStaleness(t *testing.T) {
	stale := time.Now().Add(-3 * time.Hour)
	finder := &fakeMarketGoodFinder{
		listings: []persistence.MarketGoodListing{
			{WaypointSymbol: "X1-TEST-A1", SellPrice: 20, PurchasePrice: 15, LastUpdated: stale},
		},
	}

	var err error
	output := captureStdout(t, func() {
		err = runMarketFind(context.Background(), finder, "IRON_ORE", "X1-TEST", "buy", 1, false)
	})

	require.NoError(t, err)
	require.Contains(t, output, "DATA AGE")
	require.Contains(t, output, "X1-TEST-A1")
	require.Contains(t, output, "3.0h ago")
}

func TestFormatDataAge(t *testing.T) {
	cases := []struct {
		name string
		age  time.Duration
		want string
	}{
		{"seconds", 30 * time.Second, "just now"},
		{"minutes", 5 * time.Minute, "5m ago"},
		{"hours", 3 * time.Hour, "3.0h ago"},
		{"days", 50 * time.Hour, "2.1d ago"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, formatDataAge(tc.age))
		})
	}
}
