package main

// THE CONCURRENT-SPEND CAP IS ARMED — pinned at the composition root, not merely intended.
//
// sp-ps2oc. Three construction_supply buys landed inside 68ms, all three recording the SAME
// balance_after: each read the same pre-buy treasury and none observed the others. Any one of
// them cleared the working-capital reserve; together they landed ~75k BELOW it, deadlocking
// every income path in the fleet (contract, construction and tour all parked against their own
// correctly-behaving floors, so nothing could buy, therefore nothing could earn).
//
// sp-w3he built the cure for exactly this — an atomic reserve-check-release ledger under a
// per-player Postgres advisory lock. IT WAS NEVER REACHED. The cap's own unit tests are green
// either way: they call SetSpendLedger themselves, from fixtures. Nothing below the composition
// root can see that production never does.
//
// The history, because it is the reason this file exists rather than a one-line fix:
//
//   - 4ee47ef0 wired the ledger onto factoryCoordinatorHandler — the GOODS-FACTORY
//     coordinator, the only concurrent buyer at the time.
//   - 712b6f66 retired the goods-factory operation and deleted that handler, and with
//     it the ONE production call to SetSpendLedger. The guard, its interface, its repository and
//     all of its tests survived; only the wiring died.
//   - The surviving constructionExecutor never had it. It was given SetTreasuryReader and
//     SetCapitalWorkSensor — the two sibling setters declared in the same guards file — but not
//     this one, so reserveConcurrentSpendOrPark has been returning at its `spendLedger == nil`
//     fail-open branch on every gate buy ever made.
//
// A guard deleted by a refactor of an unrelated operation is invisible to every test that wires
// its own fixture. That is what this check exists to make impossible.
//
// It pins FIVE things, because four of them fail silently:
//
//  1. SetSpendLedger is called at all — the fail-open state that caused the incident;
//  2. it is called on the SAME executor the construction coordinator is built with, so the cap
//     lands on the object that actually buys rather than an orphan;
//  3. the contract source-buy is wired to a cap too — construction, contract and tour draw on ONE
//     treasury, and a cap that serialises only construction against itself still lets a contract
//     buy race the float (acceptance criterion 4);
//  4. both receive the SAME ledger identifier — two independent caps are two independent budgets,
//     which is the aggregate breach again one level up;
//  5. the ledger argument is not syntactically nil, the shape that satisfies the interface,
//     compiles, ships, and does nothing.
//
// It parses the composition root because nothing else can see any of this, following the gate
// fleet and shared-heavy-target checks in this same package.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// spendLedgerSetter arms the cross-operation concurrent-spend cap on the construction
	// executor.
	spendLedgerSetter = "SetSpendLedger"
	// contractSpendCapOption arms the same cap on the contract source-buy, passed as a
	// functional option to the contract workflow handler.
	contractSpendCapOption = "WithConcurrentSpendCap"
	// constructionHandlerCtor is the construction coordinator's constructor. The executor the
	// setter is called on must be one of its arguments (claim 2).
	constructionHandlerCtor = "NewRunConstructionCoordinatorHandler"
	// contractHandlerCtor is the contract workflow handler's constructor. The cap option must
	// appear among its arguments (claim 3).
	contractHandlerCtor = "NewRunWorkflowHandler"
)

// spendCapWiring is what the composition root says about the concurrent-spend cap.
type spendCapWiring struct {
	// setterCalls counts constructionExecutor.SetSpendLedger call sites. The invariant is 1.
	setterCalls int
	// setterReceiver is the identifier the setter was called ON ("" when the call is not a plain
	// x.SetSpendLedger(...) — e.g. chained off a constructor, which cannot be shown to be the
	// object the coordinator was built with).
	setterReceiver string
	// setterLedgerArg is the identifier passed as the ledger, or "" when it is not a plain
	// identifier. Claim 4 compares it against contractLedgerArg.
	setterLedgerArg string
	// setterArgNil records a syntactically nil ledger — compiles, ships, does nothing.
	setterArgNil bool

	// constructionCtorArgs is the argument identifiers of NewRunConstructionCoordinatorHandler.
	// setterReceiver must appear here (claim 2).
	constructionCtorArgs []string

	// contractOptionCalls counts WithConcurrentSpendCap call sites. The invariant is 1.
	contractOptionCalls int
	// contractLedgerArg is the identifier passed to that option.
	contractLedgerArg string
	// contractOptionArgNil records a syntactically nil ledger on the contract side.
	contractOptionArgNil bool
	// contractOptionInsideCtor is whether the option call appears among the arguments of
	// NewRunWorkflowHandler. An option constructed and dropped on the floor arms nothing.
	contractOptionInsideCtor bool
}

// analyseSpendCapWiring extracts the cap's wiring facts from Go source.
func analyseSpendCapWiring(t *testing.T, filename string, src []byte) spendCapWiring {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	require.NoError(t, err, "parse %s", filename)

	var w spendCapWiring
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch calledFuncName(call) {
		case spendLedgerSetter:
			w.setterCalls++
			w.setterReceiver = receiverName(call)
			if len(call.Args) == 1 {
				w.setterLedgerArg = argIdentName(call.Args[0])
				w.setterArgNil = isNilArg(call.Args[0])
			}
		case contractSpendCapOption:
			w.contractOptionCalls++
			if len(call.Args) == 1 {
				w.contractLedgerArg = argIdentName(call.Args[0])
				w.contractOptionArgNil = isNilArg(call.Args[0])
			}
		case constructionHandlerCtor:
			for _, arg := range call.Args {
				if name := argIdentName(arg); name != "" {
					w.constructionCtorArgs = append(w.constructionCtorArgs, name)
				}
			}
		case contractHandlerCtor:
			// Claim 3: the option must be an ARGUMENT here, not merely present in the file.
			for _, arg := range call.Args {
				if calledFuncName(arg) == contractSpendCapOption {
					w.contractOptionInsideCtor = true
				}
			}
		}
		return true
	})
	return w
}

// argIdentName reduces a call argument to its identifier, seeing through parentheses. It returns
// "" for anything that is not a plain identifier (a constructor call, a literal, a selector) —
// the right answer rather than a gap, since such a value cannot be shown to be the SAME object
// the other consumer received, which is exactly what claim 4 needs.
func argIdentName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return argIdentName(e.X)
	case *ast.Ident:
		if e.Name == "nil" {
			return ""
		}
		return e.Name
	}
	return ""
}

// THE INVARIANT. One concurrent-spend cap, armed on the executor that buys AND on the contract
// source-buy that shares its treasury.
func TestConcurrentSpendCapIsWiredArmedAtTheCompositionRoot(t *testing.T) {
	mainGoPath, _ := gatePaths(t)
	src, err := os.ReadFile(mainGoPath)
	require.NoError(t, err)

	w := analyseSpendCapWiring(t, mainGoPath, src)

	// CLAIM 1. The cap exists in production at all. This is the assertion the incident needed:
	// reserveConcurrentSpendOrPark short-circuits on a nil ledger, so without this call every
	// concurrent construction_supply buy checks only its own cost against live treasury and the
	// aggregate breach is unguarded. sp-w3he's ledger was deleted from the composition root by
	// sp-hoj8u's unrelated factory retirement and nothing noticed for the whole interval.
	require.Equal(t, 1, w.setterCalls,
		"the composition root must call %s exactly once. Without it ProductionExecutor.spendLedger is nil, reserveConcurrentSpendOrPark returns its fail-open (\"\", false) on EVERY gate buy, and N concurrent buys each pass the per-buy floor while collectively breaching the reserve — the sp-ps2oc drain (297,088 credits in 68ms, three buys recording an identical balance_after). The cap's own tests wire it from fixtures and stay green regardless, so only this check can see it",
		spendLedgerSetter)

	// CLAIM 5 (checked before 2/4 because a nil ledger makes their identifiers meaningless).
	require.False(t, w.setterArgNil,
		"%s was passed a syntactically nil ledger. That satisfies the interface, compiles, ships, and leaves the cap fail-open exactly as if the call were absent",
		spendLedgerSetter)

	// CLAIM 2. The cap landed on the executor the coordinator actually dispatches to. A setter
	// on an orphan executor reports itself wired while every real buy runs uncapped.
	require.NotEmpty(t, w.setterReceiver,
		"%s must be called on a plain identifier, so it can be tied to the executor %s was built with",
		spendLedgerSetter, constructionHandlerCtor)
	require.Contains(t, w.constructionCtorArgs, w.setterReceiver,
		"%s was called on %q, which is not among the arguments of %s. The cap is armed on an executor the construction coordinator does not use, so every gate buy still runs uncapped",
		spendLedgerSetter, w.setterReceiver, constructionHandlerCtor)

	// CLAIM 3. The contract source-buy is capped too. construction_supply, contract and tour draw
	// on ONE treasury: in the incident window PURCHASE_CARGO totalled -761,919 against SELL_CARGO
	// +326,938 with no arbitration between the three. A cap that serialises construction against
	// itself leaves a contract buy free to race the same float (acceptance criterion 4).
	require.Equal(t, 1, w.contractOptionCalls,
		"the composition root must call %s exactly once so the contract source-buy reserves against the SAME float as construction. Its per-buy floor (affordableSourceBuyLot) reads live treasury and cannot see an in-flight construction reservation, which is the aggregate breach across operations",
		contractSpendCapOption)
	require.False(t, w.contractOptionArgNil,
		"%s was passed a syntactically nil ledger, leaving the contract source-buy uncapped", contractSpendCapOption)
	require.True(t, w.contractOptionInsideCtor,
		"%s must be passed as an argument to %s. Constructed and left unused it arms nothing",
		contractSpendCapOption, contractHandlerCtor)

	// CLAIM 4. ONE cap, not two. Two independent ledgers are two independent budgets, which
	// reproduces the aggregate breach one level up: each operation would serialise against its
	// own in-flight total and neither would see the other's.
	require.NotEmpty(t, w.setterLedgerArg,
		"the ledger passed to %s must be a plain identifier so it can be shown to be the same one the contract side received", spendLedgerSetter)
	require.Equal(t, w.setterLedgerArg, w.contractLedgerArg,
		"construction was capped with %q and the contract source-buy with %q. Two ledgers are two budgets: each operation serialises only against its own in-flight spend, and the combined draw on the one shared treasury is unguarded again",
		w.setterLedgerArg, w.contractLedgerArg)
}

// TEETH. The analyser must actually reject the shapes claim 1-5 are about; a check that cannot
// fail is the failure mode this whole file is a response to. Each fragment below is the real
// wiring with exactly one defect.
func TestSpendCapWiringAnalyserRejectsEachDefect(t *testing.T) {
	const wired = `package main
func f() {
	constructionExecutor := NewProductionExecutor()
	constructionExecutor.SetSpendLedger(spendCap)
	h := goodsCmd.NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, constructionExecutor, activator, nil)
	c := contractCmd.NewRunWorkflowHandler(med, shipRepo, contractRepo, nil, contractCmd.WithConcurrentSpendCap(spendCap))
	_, _ = h, c
}`
	// The analyser agrees the correct shape is correct — otherwise every rejection below is
	// vacuous (it would "reject" everything).
	good := analyseSpendCapWiring(t, "wired.go", []byte(wired))
	require.Equal(t, 1, good.setterCalls)
	require.Equal(t, "constructionExecutor", good.setterReceiver)
	require.Equal(t, "spendCap", good.setterLedgerArg)
	require.False(t, good.setterArgNil)
	require.Contains(t, good.constructionCtorArgs, "constructionExecutor")
	require.Equal(t, 1, good.contractOptionCalls)
	require.Equal(t, "spendCap", good.contractLedgerArg)
	require.True(t, good.contractOptionInsideCtor)

	t.Run("claim 1: setter absent (the sp-ps2oc production state)", func(t *testing.T) {
		const src = `package main
func f() {
	constructionExecutor := NewProductionExecutor()
	constructionExecutor.SetTreasuryReader(ledgerTreasury)
	h := goodsCmd.NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, constructionExecutor, activator, nil)
	_ = h
}`
		require.Equal(t, 0, analyseSpendCapWiring(t, "x.go", []byte(src)).setterCalls,
			"a composition root with only the sibling setters must read as UNWIRED — this is exactly what main.go looked like during the incident")
	})

	t.Run("claim 2: cap armed on an orphan executor", func(t *testing.T) {
		const src = `package main
func f() {
	otherExecutor.SetSpendLedger(spendCap)
	h := goodsCmd.NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, constructionExecutor, activator, nil)
	_ = h
}`
		w := analyseSpendCapWiring(t, "x.go", []byte(src))
		require.Equal(t, "otherExecutor", w.setterReceiver)
		require.NotContains(t, w.constructionCtorArgs, w.setterReceiver,
			"a setter on an executor the coordinator was NOT built with must not read as wired")
	})

	t.Run("claim 3: contract option constructed but never passed", func(t *testing.T) {
		const src = `package main
func f() {
	opt := contractCmd.WithConcurrentSpendCap(spendCap)
	c := contractCmd.NewRunWorkflowHandler(med, shipRepo, contractRepo, nil)
	_, _ = opt, c
}`
		w := analyseSpendCapWiring(t, "x.go", []byte(src))
		require.Equal(t, 1, w.contractOptionCalls, "the call is present...")
		require.False(t, w.contractOptionInsideCtor, "...but not as an argument to the handler, so it arms nothing")
	})

	t.Run("claim 4: two independent ledgers", func(t *testing.T) {
		const src = `package main
func f() {
	constructionExecutor.SetSpendLedger(constructionCap)
	h := goodsCmd.NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, constructionExecutor, activator, nil)
	c := contractCmd.NewRunWorkflowHandler(med, shipRepo, contractRepo, nil, contractCmd.WithConcurrentSpendCap(contractCap))
	_, _ = h, c
}`
		w := analyseSpendCapWiring(t, "x.go", []byte(src))
		require.NotEqual(t, w.setterLedgerArg, w.contractLedgerArg,
			"two differently-named ledgers must not read as one shared cap")
	})

	t.Run("claim 5: nil ledger", func(t *testing.T) {
		const src = `package main
func f() {
	constructionExecutor.SetSpendLedger(nil)
	c := contractCmd.NewRunWorkflowHandler(med, shipRepo, contractRepo, nil, contractCmd.WithConcurrentSpendCap((*persistence.SpendReservationLedgerGORM)(nil)))
	_ = c
}`
		w := analyseSpendCapWiring(t, "x.go", []byte(src))
		require.True(t, w.setterArgNil, "a bare nil ledger must be rejected")
		require.True(t, w.contractOptionArgNil, "a typed-nil conversion must be rejected too — it satisfies the interface and ships dark")
	})
}
