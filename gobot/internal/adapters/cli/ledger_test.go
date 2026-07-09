package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
)

// sampleTransactionsResponse returns a response with one fully-attributed trade
// entry and one non-trade (contract) entry that lacks good/ship/waypoint keys.
func sampleTransactionsResponse() *queries.GetTransactionsResponse {
	ts := time.Date(2026, 7, 9, 14, 30, 0, 0, time.UTC)
	return &queries.GetTransactionsResponse{
		Total: 2,
		Transactions: []*queries.TransactionDTO{
			{
				ID:            "tx-trade",
				Timestamp:     ts,
				Type:          "SELL_CARGO",
				Category:      "TRADING_REVENUE",
				Amount:        12345,
				BalanceBefore: 1000,
				BalanceAfter:  13345,
				Description:   "Sold IRON_ORE",
				Metadata: map[string]interface{}{
					"good_symbol": "IRON_ORE",
					"ship_symbol": "TORWIND-1",
					"waypoint":    "X1-AB12-CD34",
					"units":       float64(50),
				},
			},
			{
				ID:            "tx-contract",
				Timestamp:     ts,
				Type:          "CONTRACT_FULFILLED",
				Category:      "CONTRACT_REVENUE",
				Amount:        50000,
				BalanceBefore: 13345,
				BalanceAfter:  63345,
				Description:   "Fulfilled PROCUREMENT contract",
				Metadata: map[string]interface{}{
					"contract_id": "abc123",
					"faction":     "COSMIC",
				},
			},
		},
	}
}

func TestRenderTransactionListTableShowsAttributionColumns(t *testing.T) {
	var out bytes.Buffer

	err := renderTransactionList(&out, sampleTransactionsResponse(), false)

	require.NoError(t, err)
	s := out.String()
	// New attribution column headers are present.
	require.Contains(t, s, "Good")
	require.Contains(t, s, "Ship")
	require.Contains(t, s, "Waypoint")
	// The trade row's attribution values are rendered.
	require.Contains(t, s, "IRON_ORE")
	require.Contains(t, s, "TORWIND-1")
	require.Contains(t, s, "X1-AB12-CD34")
}

func TestRenderTransactionListTableDashesEmptyAttribution(t *testing.T) {
	var out bytes.Buffer

	err := renderTransactionList(&out, sampleTransactionsResponse(), false)

	require.NoError(t, err)
	var contractLine string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "CONTRACT_FULFILLED") {
			contractLine = line
			break
		}
	}
	require.NotEmpty(t, contractLine, "expected a rendered row for the contract entry")
	// Empty attribution cells render as "-", and trade-only values do not leak in.
	require.Contains(t, contractLine, "-")
	require.NotContains(t, contractLine, "IRON_ORE")
	require.NotContains(t, contractLine, "TORWIND-1")
}

func TestRenderTransactionListHandlesNilMetadata(t *testing.T) {
	resp := &queries.GetTransactionsResponse{
		Total: 1,
		Transactions: []*queries.TransactionDTO{{
			ID:            "tx-refuel",
			Timestamp:     time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
			Type:          "REFUEL",
			Category:      "FUEL_COSTS",
			Amount:        -100,
			BalanceBefore: 200,
			BalanceAfter:  100,
			Metadata:      nil,
		}},
	}
	var out bytes.Buffer

	// Must not panic on nil metadata; the row still renders.
	require.NoError(t, renderTransactionList(&out, resp, false))
	require.Contains(t, out.String(), "REFUEL")
}

func TestRenderTransactionListEmpty(t *testing.T) {
	resp := &queries.GetTransactionsResponse{Total: 0, Transactions: nil}

	var table bytes.Buffer
	require.NoError(t, renderTransactionList(&table, resp, false))
	require.Contains(t, table.String(), "No transactions found")

	// JSON on an empty result is still a well-formed envelope with an array.
	var jsonBuf bytes.Buffer
	require.NoError(t, renderTransactionList(&jsonBuf, resp, true))
	var decoded ledgerJSONOutput
	require.NoError(t, json.Unmarshal(jsonBuf.Bytes(), &decoded))
	require.Equal(t, 0, decoded.Total)
	require.Equal(t, 0, decoded.Shown)
	require.NotNil(t, decoded.Transactions)
	require.Len(t, decoded.Transactions, 0)
}

func TestRenderTransactionListJSONRoundTrips(t *testing.T) {
	var out bytes.Buffer

	err := renderTransactionList(&out, sampleTransactionsResponse(), true)

	require.NoError(t, err)
	var decoded ledgerJSONOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &decoded))

	require.Equal(t, 2, decoded.Total)
	require.Equal(t, 2, decoded.Shown)
	require.Len(t, decoded.Transactions, 2)

	trade := decoded.Transactions[0]
	require.Equal(t, "tx-trade", trade.ID)
	require.Equal(t, "SELL_CARGO", trade.Type)
	require.Equal(t, "IRON_ORE", trade.Good)
	require.Equal(t, "TORWIND-1", trade.Ship)
	require.Equal(t, "X1-AB12-CD34", trade.Waypoint)
	require.Equal(t, 12345, trade.Amount)
	require.Equal(t, 13345, trade.BalanceAfter)
	// The full metadata map is preserved (nothing dropped).
	require.Equal(t, "IRON_ORE", trade.Metadata["good_symbol"])
}

func TestLedgerJSONUsesSnakeCaseAndOmitsEmptyAttribution(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, renderTransactionList(&out, sampleTransactionsResponse(), true))
	s := out.String()

	// snake_case, machine-friendly keys.
	require.Contains(t, s, `"good": "IRON_ORE"`)
	require.Contains(t, s, `"waypoint": "X1-AB12-CD34"`)
	require.Contains(t, s, `"balance_after":`)

	// The contract entry has no good/ship/waypoint — those keys are omitted.
	var raw struct {
		Transactions []map[string]json.RawMessage `json:"transactions"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &raw))
	require.Len(t, raw.Transactions, 2)
	contract := raw.Transactions[1]
	_, hasGood := contract["good"]
	_, hasShip := contract["ship"]
	_, hasWaypoint := contract["waypoint"]
	require.False(t, hasGood, "empty good must be omitted")
	require.False(t, hasShip, "empty ship must be omitted")
	require.False(t, hasWaypoint, "empty waypoint must be omitted")
}

func TestMetaString(t *testing.T) {
	m := map[string]interface{}{"good_symbol": "IRON_ORE", "units": float64(50)}

	require.Equal(t, "IRON_ORE", metaString(m, "good_symbol"))
	require.Equal(t, "", metaString(m, "ship_symbol"))   // absent key
	require.Equal(t, "", metaString(m, "units"))         // non-string value
	require.Equal(t, "", metaString(nil, "good_symbol")) // nil map
}
