package main

// THE SHARED HEAVY TARGET: one instance, two consumers — pinned, not merely commented.
//
// The fleet-growth coordinator SPENDS the heavy accumulation and the sensing buy-floor WITHHOLDS it. Both
// read "which yard are we saving toward, and what does it ask" through *one*
// shipyardQueries.HeavyTargetFinder. Two instances would not fail loudly: each would resolve a
// perfectly valid target from the same tables, and they would simply disagree — the reservation
// holding credits back for yard A while the purchase aims at yard B, so treasury tops out below what
// we are actually asked and a 1.5–2.9M heavy is never bought. That is the exact failure the shared
// instance exists to prevent, and until now the only thing enforcing it was a comment in main.go.
//
// This check parses the composition root itself, because nothing else can see it: both consumers
// have their own passing unit tests against fakes, and those tests are green whether main.go passes
// one finder or two.
//
// It is deliberately strict about SHAPE — exactly one construction, bound to one name, and that same
// name handed to both consumers. Re-binding through an alias (b := a) would fail this test even
// though it is harmless, which is the correct trade: the invariant main.go claims is that a second
// instance is CONSPICUOUS, and an alias is precisely the edit that makes it stop being so.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	heavyTargetConstructor = "NewHeavyTargetFinder"
	// The two consumers, by constructor name (package alias stripped): the SPENDER and the WITHHOLDER.
	heavyTargetSpender    = "NewFleetGrowthCoordinatorHandler"
	heavyTargetWithholder = "NewHeavyReservePort"
)

// heavyTargetWiring is what the composition root says about the shared heavy target.
type heavyTargetWiring struct {
	// constructions counts every NewHeavyTargetFinder call site. The invariant is 1.
	constructions int
	// instanceName is the identifier the sole construction is bound to ("" if it is not a simple
	// assignment, e.g. it was constructed inline inside a consumer's argument list).
	instanceName string
	// identArgsByConsumer maps each consumer constructor to the plain identifiers it was passed.
	// A consumer handed an inline construction contributes no identifier here, which is what makes
	// the second-instance edit visible.
	identArgsByConsumer map[string][]string
	// callsByConsumer counts each consumer's call sites: a SECOND heavy buyer or reserve port, built
	// with its own finder, is the same divergence wearing a different hat.
	callsByConsumer map[string]int
}

// analyseHeavyTargetWiring extracts the wiring facts from Go source.
func analyseHeavyTargetWiring(t *testing.T, filename string, src []byte) heavyTargetWiring {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	require.NoError(t, err, "parse %s", filename)

	w := heavyTargetWiring{
		identArgsByConsumer: map[string][]string{},
		callsByConsumer:     map[string]int{},
	}

	ast.Inspect(file, func(n ast.Node) bool {
		// The construction, and the name it is bound to.
		if assign, ok := n.(*ast.AssignStmt); ok {
			for i, rhs := range assign.Rhs {
				if calledFuncName(rhs) != heavyTargetConstructor {
					continue
				}
				if i < len(assign.Lhs) {
					if ident, ok := assign.Lhs[i].(*ast.Ident); ok {
						w.instanceName = ident.Name
					}
				}
			}
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch calledFuncName(call) {
		case heavyTargetConstructor:
			w.constructions++
		case heavyTargetSpender, heavyTargetWithholder:
			name := calledFuncName(call)
			w.callsByConsumer[name]++
			for _, arg := range call.Args {
				if ident, ok := arg.(*ast.Ident); ok {
					w.identArgsByConsumer[name] = append(w.identArgsByConsumer[name], ident.Name)
				}
			}
		}
		return true
	})
	return w
}

// calledFuncName reduces a call expression to the bare function name, package qualifier stripped, so
// the check survives an import-alias rename (shipyardQuery → sq) without going quietly blind.
func calledFuncName(expr ast.Expr) string {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return ""
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel.Name
	case *ast.Ident:
		return fun.Name
	}
	return ""
}

// THE INVARIANT. Exactly one HeavyTargetFinder is constructed in the composition root, and that same
// instance reaches both the spender and the withholder.
func TestSharedHeavyTargetIsOneInstanceServingBothConsumers(t *testing.T) {
	mainGoPath, _ := gatePaths(t)
	src, err := os.ReadFile(mainGoPath)
	require.NoError(t, err)

	w := analyseHeavyTargetWiring(t, mainGoPath, src)

	require.Equal(t, 1, w.constructions,
		"the composition root must construct EXACTLY ONE %s. Two instances do not fail loudly — each resolves a valid target from the same tables and they simply disagree, so the reservation holds credits back for one yard while the buy aims at another and the heavy is never bought",
		heavyTargetConstructor)
	require.NotEmpty(t, w.instanceName,
		"the sole %s must be bound to a name and shared; constructing it inline inside a consumer's argument list is how the second instance gets added without anyone noticing",
		heavyTargetConstructor)

	require.Equal(t, 1, w.callsByConsumer[heavyTargetSpender],
		"exactly one %s: a second heavy buyer would need its own target and reintroduce the divergence", heavyTargetSpender)
	require.Equal(t, 1, w.callsByConsumer[heavyTargetWithholder],
		"exactly one %s: a second reserve port would need its own target and reintroduce the divergence", heavyTargetWithholder)

	require.True(t, slices.Contains(w.identArgsByConsumer[heavyTargetSpender], w.instanceName),
		"the SPENDER (%s) must be handed the shared %q; it is not, so the heavy buyer is aiming at a target the reservation does not know about",
		heavyTargetSpender, w.instanceName)
	require.True(t, slices.Contains(w.identArgsByConsumer[heavyTargetWithholder], w.instanceName),
		"the WITHHOLDER (%s) must be handed the shared %q; it is not, so sensing is saving toward a yard the buy is not targeting",
		heavyTargetWithholder, w.instanceName)
}

// PROOF THE CHECK HAS TEETH — a green primary test must mean "one instance", not "the parser matched
// nothing". A second finder built for one consumer is the exact edit this exists to catch, and it is
// caught twice over: the construction count rises AND the consumer no longer receives the shared name.
func TestSharedHeavyTargetDetectionCatchesASecondInstance(t *testing.T) {
	diverged := []byte(`package main

func wire() {
	heavyTargetFinder := shipyardQuery.NewHeavyTargetFinder(inv, ranker, ships, nil)
	_ = grpc.NewFleetGrowthCoordinatorHandler(srv, api, ledger, ships, med, wps, evts, yards, heavyTargetFinder, lanes, txns)
	_ = parkedSensingAdapters.NewHeavyReservePort(census, shipyardQuery.NewHeavyTargetFinder(inv, ranker, ships, nil), caps)
}
`)

	w := analyseHeavyTargetWiring(t, "diverged.go", diverged)

	require.Equal(t, 2, w.constructions, "a second construction must be counted")
	require.Equal(t, "heavyTargetFinder", w.instanceName)
	require.True(t, slices.Contains(w.identArgsByConsumer[heavyTargetSpender], w.instanceName),
		"the spender still holds the shared instance in this scenario")
	require.False(t, slices.Contains(w.identArgsByConsumer[heavyTargetWithholder], w.instanceName),
		"the withholder was handed its OWN finder — the divergence must be visible as a missing shared name, not just as a count")
}

// The same check must also catch the quieter shape: one finder, but a consumer that simply was never
// handed it (a refactor that dropped the argument). The count stays at 1, so only the consumer-side
// assertion can see it.
func TestSharedHeavyTargetDetectionCatchesAConsumerLeftUnwired(t *testing.T) {
	dropped := []byte(`package main

func wire() {
	heavyTargetFinder := shipyardQuery.NewHeavyTargetFinder(inv, ranker, ships, nil)
	_ = grpc.NewFleetGrowthCoordinatorHandler(srv, api, ledger, ships, med, wps, evts, yards, heavyTargetFinder, lanes, txns)
	_ = parkedSensingAdapters.NewHeavyReservePort(census, nil, caps)
}
`)

	w := analyseHeavyTargetWiring(t, "dropped.go", dropped)

	require.Equal(t, 1, w.constructions, "the count alone cannot see this shape")
	require.False(t, slices.Contains(w.identArgsByConsumer[heavyTargetWithholder], w.instanceName),
		"a consumer left unwired must still be caught")
}
