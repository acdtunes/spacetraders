package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// sinkDepthWiring is the helper that gives the shared absorption ledger its crush prior.
	sinkDepthWiring = "configureSinkDepthScaling"
	// absorptionHandoff is how an engine takes the shared ledger. Every function that makes this
	// handoff must configure the prior first, or that engine nets against a ledger running a
	// prior the operator did not choose.
	absorptionHandoff = "SetAbsorptionLedger"
	// sinkDepthResolver is the config resolution the wiring must read, so a refit or the kill
	// switch reaches the ledger.
	sinkDepthResolver = "ResolvedSinkDepthScaling"
	// sinkDepthSetter is the ledger-side setter the wiring exists to call.
	sinkDepthSetter = "SetSinkDepthScaling"
)

// THE INVARIANT. The depth prior is a property of the LEDGER, not of any one engine, and the
// engines share one instance — so wiring that lives beside a single engine is a fact about boot
// ORDER masquerading as a rule. Pinning it to the handoff means a reordered, retired or newly
// added engine cannot end up netting against a prior the operator did not choose.
//
// This is a composition-root check because nothing else can see the gap: the ledger's own tests
// inject a policy, so they pass whether or not the daemon ever reads config.
func TestSinkDepthPriorIsConfiguredAtEveryAbsorptionHandoff(t *testing.T) {
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
		if fn.Name.Name == sinkDepthWiring {
			wiringBody = fn
			continue
		}
		var configures, hands bool
		ast.Inspect(fn, func(n ast.Node) bool {
			if called, ok := callee(n).(*ast.SelectorExpr); ok {
				switch called.Sel.Name {
				case sinkDepthWiring:
					configures = true
				case absorptionHandoff:
					hands = true
				}
			}
			return true
		})
		if hands {
			handoffFuncs[fn.Name.Name] = configures
		}
	}

	require.NotNil(t, wiringBody, "%s must exist in %s", sinkDepthWiring, coordinatorWiringFile)
	require.NotEmpty(t, handoffFuncs, "no engine takes the shared absorption ledger — the check has lost its subject")
	for name, configures := range handoffFuncs {
		require.True(t, configures, "%s hands out the shared absorption ledger without calling %s", name, sinkDepthWiring)
	}

	var setsFromConfig bool
	ast.Inspect(wiringBody, func(n ast.Node) bool {
		sel, ok := callee(n).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != sinkDepthSetter {
			return true
		}
		for _, arg := range n.(*ast.CallExpr).Args {
			if inner, ok := callee(arg).(*ast.SelectorExpr); ok && inner.Sel.Name == sinkDepthResolver {
				setsFromConfig = true
			}
		}
		return true
	})
	require.True(t, setsFromConfig,
		"%s must call %s with %s so the prior stays operator-tunable", sinkDepthWiring, sinkDepthSetter, sinkDepthResolver)
}
