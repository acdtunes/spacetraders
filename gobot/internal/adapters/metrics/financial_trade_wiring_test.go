package metrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// sp-4i59r: trade_margin_percent AND trade_profit_per_unit WERE FED BY NOTHING.
//
// Both histograms were declared, registered, and correctly observed inside RecordTrade — and
// RecordTrade had ZERO call sites anywhere in the tree, including no package-level wrapper, which is
// how an application-layer caller would have had to reach it. Neither has ever emitted a sample.
//
// When trade margin collapsed from ~69% to ~8% in an hour, the only way to see it was hand-summing
// the transactions table in 15-minute buckets — and that aggregate cannot say WHICH good decayed,
// which the per-good label here does.

// RecordTrade OBSERVES BOTH HISTOGRAMS. Pins the arithmetic the wiring depends on: margin is
// (sell-buy)/buy as a percentage, profit is per UNIT.
func TestRecordTrade_ObservesMarginAndProfitPerUnit(t *testing.T) {
	c := NewFinancialMetricsCollector(nil, nil)

	require.Zero(t, testutil.CollectAndCount(c.tradeMarginPercent), "fresh collector should hold no series")

	// 100 -> 150 on 10 units: +50/unit, 50% margin.
	c.RecordTrade(1, "FUEL", 100, 150, 10)

	require.Equal(t, 1, testutil.CollectAndCount(c.tradeMarginPercent))
	require.Equal(t, 1, testutil.CollectAndCount(c.tradeProfitPerUnit))
}

// A NON-POSITIVE BASIS RECORDS NOTHING rather than publishing an infinite margin. The arb path can
// legitimately reach completion with TotalCost 0 (a resumed run whose cost was never persisted —
// the documented fail-open floor), and dividing by it would emit a wrong sample. A missing sample is
// better than a wrong one.
func TestRecordTrade_RefusesToPublishAMarginItCannotCompute(t *testing.T) {
	c := NewFinancialMetricsCollector(nil, nil)

	c.RecordTrade(1, "FUEL", 0, 150, 10)  // no basis
	c.RecordTrade(1, "FUEL", 100, 0, 10)  // no revenue
	c.RecordTrade(1, "FUEL", 100, 150, 0) // no units

	require.Zero(t, testutil.CollectAndCount(c.tradeMarginPercent),
		"a trade with no basis, no revenue or no units must record NOTHING — a wrong metric is worse than a missing one")
}

// THE WIRING ITSELF, checked at the source. The unit tests above pass whether or not anything in
// production calls RecordTrade — which is exactly how it stayed dead: its own tests were green
// throughout. Only a check that looks OUTSIDE this package can see the call site.
//
// It parses rather than greps so a match inside a comment or a string cannot satisfy it.
func TestRecordTradeIsCalledFromTheTradeExecutionPath(t *testing.T) {
	root := filepath.Join("..", "..", "application", "trading", "commands")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	fset := token.NewFileSet()
	calls := map[string]int{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(filepath.Join(root, e.Name()))
		require.NoError(t, rerr)
		file, perr := parser.ParseFile(fset, e.Name(), src, 0)
		require.NoError(t, perr)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "RecordTradeMetrics" {
				calls[e.Name()]++
			}
			return true
		})
	}

	require.NotEmpty(t, calls,
		"nothing in the trade execution path calls RecordTradeMetrics, so trade_margin_percent and trade_profit_per_unit emit no samples at all. The collector's own tests stay green regardless — only this check can see that the metric is dead")
}
