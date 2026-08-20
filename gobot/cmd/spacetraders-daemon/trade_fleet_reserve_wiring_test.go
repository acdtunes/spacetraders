package main

// THE COLD-START RESERVE IS ARMED — pinned at the composition root, not merely intended.
//
// sp-9bacx. The trade-fleet coordinator derives each tour launch's working-capital reserve
// from live treasury, so a fresh cold start (treasury ~147.5k, under the mature 150k default)
// launches on the immutable anti-stall floor instead of resolving max-spend to 0 and exiting
// capital_denied forever. That derivation reads through an OPTIONAL port: with no reader wired
// resolveTourReserve fails closed to the old default — which is the deadlock, silently, and
// every unit test stays green because they inject their own reader. Only the composition root
// can be asked whether production has one, so this asks it, following the concurrent-spend-cap
// check in this same package.
//
// It keys off SetTourLauncher rather than a name: the launcher is what makes an object THE
// trade-fleet coordinator, so a rename cannot quietly move the pin onto some other handler.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	tourLauncherSetter   = "SetTourLauncher"
	treasuryReaderSetter = "SetTreasuryReader"
)

func TestTradeFleetCoordinatorReadsTreasuryAtTheCompositionRoot(t *testing.T) {
	mainGoPath, _ := gatePaths(t)
	src, err := os.ReadFile(mainGoPath)
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainGoPath, src, 0)
	require.NoError(t, err, "parse %s", mainGoPath)

	coordinator := ""
	readerArg, readerArgNil, armed := "", false, false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		switch calledFuncName(call) {
		case tourLauncherSetter:
			coordinator = receiverName(call)
		case treasuryReaderSetter:
			if coordinator != "" && receiverName(call) == coordinator {
				armed = true
				readerArg, readerArgNil = argIdentName(call.Args[0]), isNilArg(call.Args[0])
			}
		}
		return true
	})

	require.NotEmpty(t, coordinator,
		"no %s call found, so the trade-fleet coordinator cannot be identified", tourLauncherSetter)
	require.True(t, armed,
		"%s.%s is never called. resolveTourReserve then keeps every launch on the tour's 150k mature default, and a cold start below it deadlocks at capital_denied with the treasury flat — the sp-9bacx incident",
		coordinator, treasuryReaderSetter)
	require.False(t, readerArgNil,
		"%s was passed a syntactically nil reader: it compiles, ships, and leaves the reserve on the default exactly as if the call were absent", treasuryReaderSetter)
	require.NotEmpty(t, readerArg, "the reader passed to %s must be a plain identifier", treasuryReaderSetter)
}
