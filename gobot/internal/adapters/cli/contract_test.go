package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
)

// fakeContractStore stands in for the contracts table. Both verbs read the SAME
// rows, so an id `list` can print but `get` cannot resolve fails a test.
type fakeContractStore struct {
	contracts []persistence.ContractModel
}

func (f *fakeContractStore) ListContracts(ctx context.Context, playerID int) ([]persistence.ContractModel, error) {
	return f.contracts, nil
}

func (f *fakeContractStore) FindContractsByIDPrefix(ctx context.Context, prefix string) ([]persistence.ContractModel, error) {
	var matches []persistence.ContractModel
	for _, m := range f.contracts {
		if strings.HasPrefix(m.ID, prefix) {
			matches = append(matches, m)
		}
	}
	return matches, nil
}

func deliveriesJSON(t *testing.T, deliveries []contract.Delivery) string {
	t.Helper()
	data, err := marshalDeliveries(deliveries)
	require.NoError(t, err)
	return data
}

func TestRunContractListShowsDeadlineAndRemaining(t *testing.T) {
	future := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	store := &fakeContractStore{
		contracts: []persistence.ContractModel{
			{
				ID:                 "contract-abc123456789",
				FactionSymbol:      "COSMIC",
				Type:               "PROCUREMENT",
				Accepted:           true,
				Fulfilled:          false,
				Deadline:           future,
				PaymentOnAccepted:  1000,
				PaymentOnFulfilled: 9000,
				DeliveriesJSON:     deliveriesJSON(t, []contract.Delivery{{TradeSymbol: "IRON_ORE", UnitsRequired: 100, UnitsFulfilled: 10}}),
			},
		},
	}

	rows, err := listContractRows(context.Background(), store, 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	row := rows[0]
	require.Equal(t, "contract-", row.ShortID)
	require.True(t, row.Accepted)
	require.False(t, row.Fulfilled)
	require.Equal(t, 10000, row.TotalPayment)
	require.Greater(t, row.TimeRemaining, time.Duration(0))
	require.LessOrEqual(t, row.TimeRemaining, 48*time.Hour)
}

func TestRunContractListMarksOverdueDeadline(t *testing.T) {
	past := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	store := &fakeContractStore{
		contracts: []persistence.ContractModel{
			{
				ID:             "overdue-contract",
				Deadline:       past,
				DeliveriesJSON: deliveriesJSON(t, nil),
			},
		},
	}

	rows, err := listContractRows(context.Background(), store, 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Overdue)
	require.LessOrEqual(t, rows[0].TimeRemaining, time.Duration(0))
}

func TestRunContractGetReturnsDeliveryProgressAndPayments(t *testing.T) {
	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	model := persistence.ContractModel{
		ID:                 "contract-full-id-xyz",
		FactionSymbol:      "COSMIC",
		Type:               "PROCUREMENT",
		Accepted:           true,
		Fulfilled:          false,
		Deadline:           future,
		PaymentOnAccepted:  500,
		PaymentOnFulfilled: 4500,
		DeliveriesJSON: deliveriesJSON(t, []contract.Delivery{
			{TradeSymbol: "IRON_ORE", DestinationSymbol: "X1-AB-C", UnitsRequired: 100, UnitsFulfilled: 40},
		}),
	}
	store := &fakeContractStore{contracts: []persistence.ContractModel{model}}

	detail, err := getContractDetail(context.Background(), store, "contract-full-id-xyz")
	require.NoError(t, err)
	require.Equal(t, "contract-full-id-xyz", detail.ID)
	require.Equal(t, 500, detail.PaymentOnAccepted)
	require.Equal(t, 4500, detail.PaymentOnFulfilled)
	require.Len(t, detail.Deliveries, 1)
	require.Equal(t, "IRON_ORE", detail.Deliveries[0].TradeSymbol)
	require.Equal(t, 100, detail.Deliveries[0].UnitsRequired)
	require.Equal(t, 40, detail.Deliveries[0].UnitsFulfilled)
}

// The two verbs must agree on the identifier: `contract get` has to resolve every id
// `contract list` prints, or the table advertises ids the operator cannot look up.
// Real contract ids are 25-char cuids and the table prints a 9-char abbreviation.
func TestContractGetResolvesEveryIDContractListPrints(t *testing.T) {
	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	store := &fakeContractStore{contracts: []persistence.ContractModel{
		{
			ID: "cms1ww9it000108l4ce4gd2ka", Type: "PROCUREMENT", FactionSymbol: "COSMIC",
			Accepted: true, Deadline: future, PaymentOnAccepted: 63885, PaymentOnFulfilled: 255540,
			DeliveriesJSON: deliveriesJSON(t, []contract.Delivery{{TradeSymbol: "IRON_ORE", UnitsRequired: 200}}),
		},
		{
			ID: "cms1wualv000208l4f2h9x7bc", Type: "PROCUREMENT", FactionSymbol: "COSMIC",
			Accepted: true, Fulfilled: true, Deadline: future, PaymentOnAccepted: 15768, PaymentOnFulfilled: 63072,
			DeliveriesJSON: deliveriesJSON(t, nil),
		},
		{
			ID: "cms1v602e000308l4b1c8m3qd", Type: "PROCUREMENT", FactionSymbol: "COSMIC",
			Accepted: true, Fulfilled: true, Deadline: future, PaymentOnAccepted: 5576, PaymentOnFulfilled: 22304,
			DeliveriesJSON: deliveriesJSON(t, nil),
		},
	}}

	rows, err := listContractRows(context.Background(), store, 1)
	require.NoError(t, err)
	require.Len(t, rows, 3, "every listed contract must be exercised")

	for _, row := range rows {
		printed := row.ShortID // exactly what the `contract list` table column carries

		detail, err := getContractDetail(context.Background(), store, printed)

		require.NoErrorf(t, err, "contract get must resolve the id contract list prints: %q", printed)
		require.Equal(t, row.ID, detail.ID, "the printed id must resolve to the contract that produced it")
		require.Equal(t, row.TotalPayment, detail.PaymentOnAccepted+detail.PaymentOnFulfilled)
	}
}

// An abbreviation shared by several contracts must be reported with the full ids to
// retype, never silently resolved to an arbitrary one.
func TestContractGetRejectsAmbiguousIDPrefix(t *testing.T) {
	store := &fakeContractStore{contracts: []persistence.ContractModel{
		{ID: "cms1ww9it000108l4ce4gd2ka", DeliveriesJSON: deliveriesJSON(t, nil)},
		{ID: "cms1ww9it000208l4h7k2p9wz", DeliveriesJSON: deliveriesJSON(t, nil)},
	}}

	_, err := getContractDetail(context.Background(), store, "cms1ww9it")

	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous")
	require.Contains(t, err.Error(), "cms1ww9it000108l4ce4gd2ka")
	require.Contains(t, err.Error(), "cms1ww9it000208l4h7k2p9wz")
}

func TestRunContractGetNotFoundReturnsError(t *testing.T) {
	store := &fakeContractStore{}

	_, err := getContractDetail(context.Background(), store, "missing")
	require.Error(t, err)
}

type fakeDemandMiner struct {
	candidates []persistence.DemandCandidate
	gotHome    string
	gotOpts    persistence.DemandMinerOptions
}

func (f *fakeDemandMiner) Mine(ctx context.Context, homeSystem string, playerID int, eraID *int, opts persistence.DemandMinerOptions) ([]persistence.DemandCandidate, error) {
	f.gotHome = homeSystem
	f.gotOpts = opts
	return f.candidates, nil
}

func TestRunContractDemandRendersTableWithEligibilityAndUnknownHomeAsk(t *testing.T) {
	miner := &fakeDemandMiner{candidates: []persistence.DemandCandidate{
		{Good: "IRON_ORE", ContractCount: 3, DemandUnits: 300, RecurrenceWindowDays: 4,
			ForeignMarket: "X1-FOREIGN-B1", ForeignSystem: "X1-FOREIGN", ForeignAsk: 40,
			HomeAsk: 90, HomeAskKnown: true, ProjectedSavingsPerUnit: 50, StockEligible: true},
		{Good: "COPPER_ORE", ContractCount: 2, DemandUnits: 40,
			ForeignMarket: "X1-ORE-C1", ForeignSystem: "X1-ORE", ForeignAsk: 20, HomeAskKnown: false},
	}}

	var buf bytes.Buffer
	err := runContractDemand(context.Background(), miner, &buf, "X1-HOME", 1, nil,
		persistence.DemandMinerOptions{MinRecurrence: 2}, false)
	require.NoError(t, err)

	out := buf.String()
	require.Contains(t, out, "X1-HOME")
	require.Contains(t, out, "IRON_ORE")
	require.Contains(t, out, "X1-FOREIGN-B1")
	require.Contains(t, out, "COPPER_ORE")
	require.Contains(t, out, "yes") // IRON_ORE is stock-eligible
	require.Contains(t, out, "no")  // COPPER_ORE (home ask unknown) is not

	// The renderer forwards the home system and options to the miner.
	require.Equal(t, "X1-HOME", miner.gotHome)
	require.Equal(t, 2, miner.gotOpts.MinRecurrence)
}

func TestRunContractDemandJSONOutput(t *testing.T) {
	miner := &fakeDemandMiner{candidates: []persistence.DemandCandidate{
		{Good: "IRON_ORE", ForeignAsk: 40, HomeAsk: 90, HomeAskKnown: true, ProjectedSavingsPerUnit: 50, StockEligible: true},
	}}

	var buf bytes.Buffer
	err := runContractDemand(context.Background(), miner, &buf, "X1-HOME", 1, nil,
		persistence.DemandMinerOptions{}, true)
	require.NoError(t, err)

	out := buf.String()
	require.Contains(t, out, "\"good\": \"IRON_ORE\"")
	require.Contains(t, out, "\"stock_eligible\": true")
}

func TestRunContractDemandEmptyMessage(t *testing.T) {
	miner := &fakeDemandMiner{candidates: nil}

	var buf bytes.Buffer
	err := runContractDemand(context.Background(), miner, &buf, "X1-HOME", 1, nil,
		persistence.DemandMinerOptions{}, false)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "No recurring contract demand")
}
