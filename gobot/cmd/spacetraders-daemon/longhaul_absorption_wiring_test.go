package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// longHaulAbsorptionSetter arms the sink-depth consult on the long-haul arb worker.
	longHaulAbsorptionSetter = "SetAbsorptionLedger"
	// longHaulWorkerCtor is the worker's constructor. The setter must be called on the value it
	// returns, or the consult is armed on an object nothing dispatches to.
	longHaulWorkerCtor = "NewLongHaulArbWorkerHandler"
	// absorptionLedgerCtor builds the one ledger every engine shares. The identifier passed to
	// the setter must be the one assigned from it.
	absorptionLedgerCtor = "NewAbsorptionLedger"
)

// THE INVARIANT (sp-kw2em). The long-haul sink-depth clamp must be ARMED at the composition
// root.
//
// Its predecessor, SetAbsorptionHeadroom, had ZERO call sites for its entire lifetime — not in
// production, not in tests. The field stayed nil, selectHauls passed the "not consulted"
// sentinel on every candidate, and a guard that was built, consumed and unit-tested never once
// applied to a real buy. The clamp's own tests passed throughout, because they inject a stub;
// only a check at the composition root can see an unarmed one.
//
// The bead notes this handler is NOT on the construction executor, so it needs its own check
// rather than a row in executor_guard_wiring_test.go.
func TestLongHaulAbsorptionConsultIsWiredArmedAtTheCompositionRoot(t *testing.T) {
	mainGoPath, _ := gatePaths(t)
	src, err := os.ReadFile(mainGoPath)
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainGoPath, src, 0)
	require.NoError(t, err, "parse %s", mainGoPath)

	var (
		setterCalls   int
		setterRecv    string
		setterArg     string
		setterArgNil  bool
		ledgerVarName string
		workerVarName string
	)

	// TWO PASSES, deliberately. ast.Inspect walks in source order, and a sibling engine's
	// SetAbsorptionLedger call appears EARLIER in main.go than the long-haul worker's assignment
	// — so a single pass cannot filter by receiver, because the worker's identifier is not known
	// yet when that earlier call is visited. Collect the identifiers first, then count.
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		switch calledFuncName(call) {
		case absorptionLedgerCtor:
			ledgerVarName = argIdentName(assign.Lhs[0])
		case longHaulWorkerCtor:
			workerVarName = argIdentName(assign.Lhs[0])
		}
		return true
	})

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || calledFuncName(call) != longHaulAbsorptionSetter {
			return true
		}
		// Sibling engines share the method name; only the long-haul worker's call counts.
		if receiverName(call) != workerVarName {
			return true
		}
		setterCalls++
		setterRecv = receiverName(call)
		if len(call.Args) == 1 {
			setterArg = argIdentName(call.Args[0])
			setterArgNil = isNilArg(call.Args[0])
		}
		return true
	})

	require.NotEmpty(t, workerVarName,
		"could not find the %s assignment; if the composition root was restructured this test must move with it", longHaulWorkerCtor)
	require.NotEmpty(t, ledgerVarName,
		"could not find the %s assignment; the shared ledger must exist for the consult to be armed with it", absorptionLedgerCtor)

	require.Equal(t, 1, setterCalls,
		"the composition root must call %s on the long-haul worker exactly once; found %d. Without it "+
			"absorptionLedger stays nil, absorptionHeadroomFn returns nil, selectHauls sizes every lane with "+
			"the not-consulted sentinel, and the sink-depth clamp never applies to a single real buy — which "+
			"is exactly the state sp-kw2em found, for the whole life of the predecessor setter",
		longHaulAbsorptionSetter, setterCalls)

	require.False(t, setterArgNil,
		"%s was passed a syntactically nil ledger. That compiles, ships, and leaves the clamp inert in "+
			"precisely the way a missing call would", longHaulAbsorptionSetter)

	require.Equal(t, workerVarName, setterRecv,
		"%s was called on %q, not on the long-haul worker %q built by %s. A consult armed on an object the "+
			"mediator does not dispatch to reports itself wired while every real buy runs unclamped",
		longHaulAbsorptionSetter, setterRecv, workerVarName, longHaulWorkerCtor)

	require.Equal(t, ledgerVarName, setterArg,
		"%s was passed %q, but the shared absorption ledger is %q. Long-haul must consult the SAME ledger "+
			"the other engines write their in-flight units to, or it sees an empty pool and the clamp is a "+
			"no-op by construction", longHaulAbsorptionSetter, setterArg, ledgerVarName)
}
