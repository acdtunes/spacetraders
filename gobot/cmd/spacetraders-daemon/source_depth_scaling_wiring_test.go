package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// sourceDepthWiring is the helper that gives the shared compression ledger its pacing prior
	// and the listing-breadth lookup that prior reads.
	sourceDepthWiring = "configureSourceDepthScaling"
	// laneCooldownHandoff is how an engine takes the shared compression ledger. A function making
	// this handoff without the wiring leaves the prior with no breadth to read.
	laneCooldownHandoff = "SetLaneImpactModel"
	// sourceDepthResolver is the config resolution the wiring must read, so a refit or the kill
	// switch reaches the ledger.
	sourceDepthResolver = "ResolvedSourceDepthScaling"
	// sourceDepthSetter is the ledger-side setter the wiring exists to call.
	sourceDepthSetter = "SetSourceDepthScaling"

	coordinatorWiringFile = "coordinator_wiring.go"
)

// THE INVARIANT. The breadth lookup is a property of the LEDGER, one instance shared by every
// engine and by the gate-feed consult, so wiring that lives beside a single engine is a fact about
// boot ORDER masquerading as a rule. Pinning it to the handoff means a reordered, retired or newly
// added engine cannot leave the prior blind — which degrades silently, since an unread breadth
// paces exactly like a market nobody has scanned.
//
// This is a composition-root check because nothing else can see the gap: the ledger's own tests
// inject a reader, so they pass whether or not the daemon ever wires one.
func TestSourceDepthPriorIsConfiguredAtEveryLaneCooldownHandoff(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, coordinatorWiringFile, nil, 0)
	require.NoError(t, err, "parse %s", coordinatorWiringFile)

	var wiringBody *ast.FuncDecl
	handoffFuncs := map[string]bool{}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == sourceDepthWiring {
			wiringBody = fn
			continue
		}
		var wires, hands bool
		ast.Inspect(fn, func(n ast.Node) bool {
			if called, ok := callee(n).(*ast.SelectorExpr); ok {
				switch called.Sel.Name {
				case sourceDepthWiring:
					wires = true
				case laneCooldownHandoff:
					hands = true
				}
			}
			return true
		})
		if hands {
			handoffFuncs[fn.Name.Name] = wires
		}
	}

	require.NotNil(t, wiringBody, "%s must exist in %s", sourceDepthWiring, coordinatorWiringFile)
	require.NotEmpty(t, handoffFuncs, "no engine takes the shared compression ledger — the check has lost its subject")
	for name, wires := range handoffFuncs {
		require.True(t, wires, "%s hands out the shared compression ledger without calling %s", name, sourceDepthWiring)
	}

	var setsFromConfig bool
	ast.Inspect(wiringBody, func(n ast.Node) bool {
		sel, ok := callee(n).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != sourceDepthSetter {
			return true
		}
		for _, arg := range n.(*ast.CallExpr).Args {
			if inner, ok := callee(arg).(*ast.SelectorExpr); ok && inner.Sel.Name == sourceDepthResolver {
				setsFromConfig = true
			}
		}
		return true
	})
	require.True(t, setsFromConfig,
		"%s must call %s with %s so the prior stays operator-tunable", sourceDepthWiring, sourceDepthSetter, sourceDepthResolver)
}

// callee returns the function expression of a call node, or nil for anything else.
func callee(n ast.Node) ast.Expr {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil
	}
	return call.Fun
}
