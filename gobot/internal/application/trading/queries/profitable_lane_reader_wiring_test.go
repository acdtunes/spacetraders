package queries

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// A RETUNED CAP MUST ACTUALLY REACH THE CENSUS — the wiring scan below proves the setter is CALLED,
// this proves the field it sets is CONSULTED. Both quotes are STRONG and dated inside the fitted
// 30-minute cap but outside a captain's tightened one, so the retune is the only thing that can move
// the count.
func TestCountProfitableLanes_ATunedFreshnessCapReachesTheCensus(t *testing.T) {
	const observedAgo = 20 * time.Minute
	require.Less(t, observedAgo, trading.DefaultRankerAgeCapStrong,
		"calibration: the fitted table still ranks a quote this age")

	strong := string(shared.ActivityLevelStrong)
	surface := func() *fakeLaneMarketReader {
		observed := time.Now().Add(-observedAgo)
		return &fakeLaneMarketReader{
			systems: map[string][]string{"X1-AA": {"X1-AA-1", "X1-AA-2"}},
			markets: map[string]*market.Market{
				"X1-AA-1": mktObservedAt(t, "X1-AA-1", observed,
					goodWithActivity(t, "FUEL", 50, 100, 50, market.TradeTypeExport, strong)),
				"X1-AA-2": mktObservedAt(t, "X1-AA-2", observed,
					goodWithActivity(t, "FUEL", 2000, 3000, 50, market.TradeTypeImport, strong)),
			},
		}
	}

	fitted := NewProfitableLaneReader(surface(), reachAllWithin(1))
	count, _, err := fitted.countProfitable(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.Equal(t, 1, count, "calibration: under the fitted table this pairing IS a lane")

	tuned := NewProfitableLaneReader(surface(), reachAllWithin(1))
	tuned.SetRankerAgeCaps(trading.RankerAgeCaps{Strong: 10 * time.Minute})
	count, readable, err := tuned.countProfitable(context.Background(), 1, []string{"X1-AA"})
	require.NoError(t, err)
	require.True(t, readable, "a row the captain's cap drops is a readable ZERO, not an outage")
	require.Zero(t, count,
		"the executor's ranker drops this row under the same table — the census must not size a hull to it")
}

// goodWithActivity builds a listing carrying an ACTIVITY level, which is what selects its freshness
// cap. The plain `good` helper leaves it unset, and unset falls to the RESTRICTED middle.
func goodWithActivity(t *testing.T, symbol string, bid, ask, volume int, tradeType market.TradeType, activity string) market.TradeGood {
	t.Helper()
	g, err := market.NewTradeGood(symbol, nil, &activity, ask, bid, volume, tradeType)
	require.NoError(t, err)
	return *g
}

// THE CENSUS AND THE EXECUTOR MUST READ ONE FRESHNESS TABLE — pinned at the composition root,
// because nothing else can see it.
//
// [trading] ranker_age_cap_minutes is a captain knob. Every executor-side reader of the table is
// handed the resolved one at boot; a census left on the zero value keeps running the fitted
// defaults. Tighten the knob and the divergence points at a spend: the ranker drops a listing the
// census still counts, so a lane nothing will ever fly raises unserved demand and buys a hull for it
// (RULINGS #4). Neither side fails when they disagree — both resolve a perfectly valid table — so
// the unit tests on either side stay green whichever way the wiring goes.
//
// The invariant is deliberately about SHAPE: every construction is bound to a name and that same
// name is handed the table in the same file. An inline construction fails this even though it may be
// harmless, which is the correct trade — a reader nobody named is a reader nobody can wire.
const (
	laneReaderCtor        = "NewProfitableLaneReader"
	laneReaderAgeCapsSetr = "SetRankerAgeCaps"
)

func TestProfitableLaneReader_EveryConstructionIsHandedTheFreshnessTable(t *testing.T) {
	root := moduleRoot(t)

	constructions := 0
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(src, []byte(laneReaderCtor+"(")) {
			return nil
		}
		constructions += assertLaneReaderWiring(t, path, src)
		return nil
	}))

	require.NotZero(t, constructions,
		"no production construction found — the scan has gone blind (renamed constructor?) and would pass an unwired census")
}

// assertLaneReaderWiring checks one production file: every census construction is bound to a name,
// and that name is handed the freshness table somewhere in the same file. Returns how many
// constructions it found, so the scan can prove it is still looking at something.
func assertLaneReaderWiring(t *testing.T, path string, src []byte) int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	require.NoError(t, err, "parse %s", path)

	boundNames := map[string]token.Pos{} // identifier -> where it was constructed
	inlineAt := []token.Pos{}            // constructions bound to no name at all
	wired := map[string]bool{}           // identifier -> was handed the table

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			bindLaneReaders(node.Lhs, node.Rhs, boundNames)
		case *ast.ValueSpec:
			bindLaneReaders(identExprs(node.Names), node.Values, boundNames)
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == laneReaderAgeCapsSetr {
				if recv, ok := sel.X.(*ast.Ident); ok {
					wired[recv.Name] = true
				}
			}
		}
		return true
	})

	// Anything constructed but not bound above is inline inside another call's argument list.
	total := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || laneCalledFuncName(call) != laneReaderCtor {
			return true
		}
		total++
		return true
	})
	if total > len(boundNames) {
		inlineAt = append(inlineAt, file.Pos())
	}

	// assert, not require: the scan reports EVERY unwired site in one run rather than stopping the
	// walk at the first.
	assert.Empty(t, inlineAt,
		"%s constructs %s inline (%d construction(s), %d named): bind it to a name and hand it %s, or the freshness table can never reach it",
		path, laneReaderCtor, total, len(boundNames), laneReaderAgeCapsSetr)

	for name, pos := range boundNames {
		assert.True(t, wired[name],
			"%s: %s built at %s is never handed the resolved freshness table (%s) — it will run the fitted defaults while the executor's ranker runs the captain's, and count lanes the ranker has already dropped",
			path, name, fset.Position(pos), laneReaderAgeCapsSetr)
	}
	return total
}

// bindLaneReaders records, for each census construction on the right-hand side, the plain identifier
// it is bound to on the left.
func bindLaneReaders(lhs, rhs []ast.Expr, into map[string]token.Pos) {
	for i, value := range rhs {
		call, ok := value.(*ast.CallExpr)
		if !ok || laneCalledFuncName(call) != laneReaderCtor || i >= len(lhs) {
			continue
		}
		if ident, ok := lhs[i].(*ast.Ident); ok {
			into[ident.Name] = call.Pos()
		}
	}
}

func identExprs(names []*ast.Ident) []ast.Expr {
	out := make([]ast.Expr, len(names))
	for i, n := range names {
		out[i] = n
	}
	return out
}

// laneCalledFuncName reduces a call to its bare function name, package qualifier stripped, so the
// scan survives an import-alias rename rather than going quietly blind.
func laneCalledFuncName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel.Name
	case *ast.Ident:
		return fun.Name
	}
	return ""
}

// moduleRoot walks up from the test's package directory to the go.mod that bounds this module, so
// the scan covers every production package and stops at the module edge.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "no go.mod above %s", dir)
		dir = parent
	}
}
