package main

// THE CONSTRUCTION EXECUTOR'S GUARD COLLABORATORS ARE ARMED — pinned as a SET, not one at a time
// (sp-773i0, generalising sp-ps2oc's spend_ledger_wiring_test.go and sp-a75fz's api-util pin).
//
// WHY A TABLE RATHER THAN THREE FILES. These setters share one receiver, one composition block
// (main.go, ~20 lines), one failure shape and one blind spot: each is OPTIONAL by design so
// fixtures can omit it, so every unit test wires its own and stays green whether or not production
// wires anything. One analyser with a row per setter is the whole of the difference between them;
// three files would be three copies of the same twenty lines, drifting.
//
// It deliberately does NOT try to be a framework over every setter in the repo. The census behind
// sp-773i0 found 168 Set* methods, of which most are domain value mutators, tunables with
// documented defaults, or seams with a global fallback — pinning those would be noise that teaches
// readers to ignore this file. The membership rule is narrow and stated in guardCollaborators
// below: a setter belongs here when its absence silently disarms a MONEY GUARD.
//
// WHAT IT CANNOT DO, stated so nobody trusts it further than it goes: it proves a call EXISTS with
// a non-nil argument on the right receiver. It cannot prove the argument is the right object, that
// the collaborator works, or that a setter with ZERO call sites should have one — an absent setter
// is invisible to a check written against the setters that are present. The census is what finds
// those; this file is what stops the present ones disappearing.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// guardCollaborator is one optional setter whose absence silently disarms a money guard.
type guardCollaborator struct {
	// setter is the method name on the construction executor.
	setter string
	// whenAbsent is what production does with the collaborator missing — the reason this row
	// exists, and the sentence a failure should make someone read.
	whenAbsent string
}

// guardCollaborators is the membership rule made concrete.
//
// SetSpendLedger is deliberately ABSENT: spend_ledger_wiring_test.go already pins it, and more
// strictly than this file could — it also ties the ledger identity to the contract side's cap so
// the two cannot become two budgets. Duplicating it here would mean two checks to keep in step.
var guardCollaborators = []guardCollaborator{
	{
		setter: "SetTreasuryReader",
		whenAbsent: "both factory money guards fall back to a live Get Agent per input tranche, and if the api client is nil too, spendFloorBreached returns its fail-OPEN branch " +
			"(`e.apiClient == nil && e.treasury == nil`) and every input buy proceeds unchecked against the working-capital reserve",
	},
	{
		setter: "SetPriceHistoryReader",
		whenAbsent: "trailingMedianAsk returns ok=false on EVERY call (`if e.priceHistory == nil`) and rescueSource refuses on false, so every rescue source-buy parks permanently — " +
			"while logging \"no trailing median at %s\", which reads as a market with no price history rather than a missing collaborator. " +
			"This row is not hypothetical: the setter had ZERO production call sites for the whole of era 6 (sp-f5lki), and the three behavioural tests of the rescue path were green throughout because each wires its own reader",
	},
	{
		setter: "SetCapitalWorkSensor",
		whenAbsent: "budgetedReserveFloor silently drops back to the flat non-contract floor (`if e.workSensor == nil { return floor }`), so trade's reserved share of deployable capital is never added — " +
			"construction and trade each conclude the other is idle and both size against the whole treasury, which is the aggregate breach the sensor exists to prevent",
	},
}

// executorWiring is what the composition root says about one setter.
type executorWiring struct {
	calls    int
	receiver string
	argNil   bool
}

// analyseExecutorWiring extracts, for each named setter, its call count, the identifier it was
// called on, and whether the argument is syntactically nil. It also collects the arguments of the
// executor constructor so a setter can be tied to the object the coordinator actually uses.
func analyseExecutorWiring(t *testing.T, filename string, src []byte, setters []string) (map[string]executorWiring, []string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	require.NoError(t, err, "parse %s", filename)

	wanted := make(map[string]bool, len(setters))
	for _, s := range setters {
		wanted[s] = true
	}

	found := make(map[string]executorWiring, len(setters))
	var executorReceivers []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calledFuncName(call)
		if name == "NewProductionExecutor" {
			// The receiver every guard setter must land on is whatever this constructor's result
			// was assigned to; captured via the enclosing assignment below.
			return true
		}
		if !wanted[name] {
			return true
		}
		w := found[name]
		w.calls++
		w.receiver = receiverName(call)
		if len(call.Args) == 1 && isNilArg(call.Args[0]) {
			w.argNil = true
		}
		found[name] = w
		return true
	})

	// The executor identifier: the LHS of `x := goodsServices.NewProductionExecutor(...)`.
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		if call, ok := assign.Rhs[0].(*ast.CallExpr); ok && calledFuncName(call) == "NewProductionExecutor" {
			if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
				executorReceivers = append(executorReceivers, ident.Name)
			}
		}
		return true
	})
	return found, executorReceivers
}

// THE INVARIANT, against the real composition root.
func TestConstructionExecutorGuardCollaboratorsAreArmed(t *testing.T) {
	mainGoPath, _ := gatePaths(t)
	src, err := os.ReadFile(mainGoPath)
	require.NoError(t, err)

	names := make([]string, 0, len(guardCollaborators))
	for _, c := range guardCollaborators {
		names = append(names, c.setter)
	}
	found, executors := analyseExecutorWiring(t, mainGoPath, src, names)

	require.Len(t, executors, 1,
		"expected exactly one NewProductionExecutor at the composition root, found %v — with more than one, 'the setter landed on an executor' stops being a single question", executors)
	executor := executors[0]

	for _, collaborator := range guardCollaborators {
		t.Run(collaborator.setter, func(t *testing.T) {
			w := found[collaborator.setter]

			require.Equal(t, 1, w.calls,
				"%s is not called at the composition root. When it is missing, %s. Every test of this guard wires its own fixture and stays green regardless, so only a check here can see it — that blind spot is what let sp-ps2oc's money cap fail open on every gate buy ever made",
				collaborator.setter, collaborator.whenAbsent)

			require.False(t, w.argNil,
				"%s was passed a syntactically nil collaborator. It satisfies the interface, compiles, ships, and leaves the guard exactly as disarmed as if the call were absent — %s",
				collaborator.setter, collaborator.whenAbsent)

			require.Equal(t, executor, w.receiver,
				"%s was called on %q, not on %q — the executor the construction coordinator is built with. A setter on an orphan executor reports itself wired while every real buy runs with the guard disarmed",
				collaborator.setter, w.receiver, executor)
		})
	}
}

// TEETH. The analyser must genuinely reject each shape above; a check that cannot fail is the
// failure mode this whole class is about.
func TestExecutorGuardWiringAnalyserRejectsEachDefect(t *testing.T) {
	names := []string{"SetTreasuryReader", "SetCapitalWorkSensor"}

	const wired = `package main
func f() {
	constructionExecutor := goodsServices.NewProductionExecutor(med, shipRepo)
	constructionExecutor.SetTreasuryReader(ledgerTreasury)
	constructionExecutor.SetCapitalWorkSensor(capitalWorkSensor)
}`
	// Non-vacuity: the correct shape must read as correct, or every rejection below is empty.
	good, executors := analyseExecutorWiring(t, "wired.go", []byte(wired), names)
	require.Equal(t, []string{"constructionExecutor"}, executors)
	require.Equal(t, 1, good["SetTreasuryReader"].calls)
	require.Equal(t, "constructionExecutor", good["SetCapitalWorkSensor"].receiver)
	require.False(t, good["SetTreasuryReader"].argNil)

	t.Run("setter deleted by an unrelated refactor", func(t *testing.T) {
		const src = `package main
func f() {
	constructionExecutor := goodsServices.NewProductionExecutor(med, shipRepo)
	constructionExecutor.SetSpendLedger(spendCap)
}`
		w, _ := analyseExecutorWiring(t, "x.go", []byte(src), names)
		require.Zero(t, w["SetTreasuryReader"].calls,
			"a composition root holding only a SIBLING setter must read as UNWIRED — this is the sp-ps2oc production state exactly")
	})

	t.Run("nil collaborator", func(t *testing.T) {
		const src = `package main
func f() {
	constructionExecutor := goodsServices.NewProductionExecutor(med, shipRepo)
	constructionExecutor.SetCapitalWorkSensor(nil)
}`
		w, _ := analyseExecutorWiring(t, "x.go", []byte(src), names)
		require.True(t, w["SetCapitalWorkSensor"].argNil, "a bare nil must be rejected")
	})

	t.Run("typed-nil collaborator", func(t *testing.T) {
		const src = `package main
func f() {
	constructionExecutor := goodsServices.NewProductionExecutor(med, shipRepo)
	constructionExecutor.SetTreasuryReader((*common.LedgerTreasury)(nil))
}`
		w, _ := analyseExecutorWiring(t, "x.go", []byte(src), names)
		require.True(t, w["SetTreasuryReader"].argNil,
			"a typed nil satisfies the interface and ships dark — it must be rejected too")
	})

	t.Run("armed on an orphan executor", func(t *testing.T) {
		const src = `package main
func f() {
	constructionExecutor := goodsServices.NewProductionExecutor(med, shipRepo)
	otherExecutor.SetTreasuryReader(ledgerTreasury)
}`
		w, executors := analyseExecutorWiring(t, "x.go", []byte(src), names)
		require.Equal(t, []string{"constructionExecutor"}, executors)
		require.Equal(t, "otherExecutor", w["SetTreasuryReader"].receiver,
			"a setter on an executor the coordinator was NOT built with must not read as wired")
	})
}
