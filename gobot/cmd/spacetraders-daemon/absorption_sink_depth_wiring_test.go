package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// sinkDepthArm is the helper that hands the shared absorption ledger its
	// depth-conditioned crush prior.
	sinkDepthArm = "armAbsorptionSinkDepth"
	// absorptionHandoff is how an engine takes the shared ledger. Every function that
	// makes this handoff must arm the prior first, or that engine nets against a ledger
	// running a prior nobody chose.
	absorptionHandoff = "SetAbsorptionLedger"
	// sinkDepthResolver is the config resolution the arm must read. Calling the setter
	// with a hand-built policy would put the knob out of the operator's reach.
	sinkDepthResolver = "ResolvedSinkDepthScaling"
	// sinkDepthSetter is the ledger-side setter the arm exists to call.
	sinkDepthSetter = "SetSinkDepthScaling"

	coordinatorWiringFile = "coordinator_wiring.go"
)

// THE INVARIANT. The depth prior is a property of the LEDGER, not of any one engine, and
// the engines share one instance — so an arm that lives beside a single engine's wiring
// is a fact about boot ORDER masquerading as a rule. Pinning it to the handoff instead
// means a reordered, retired or newly-added engine cannot leave the prior unarmed.
//
// This is a composition-root check because nothing else can see the gap: the ledger's own
// tests inject a policy, so they pass whether or not the daemon ever sets one.
func TestSinkDepthPriorIsArmedAtEveryAbsorptionHandoff(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, coordinatorWiringFile, nil, 0)
	require.NoError(t, err, "parse %s", coordinatorWiringFile)

	var armBody *ast.FuncDecl
	handoffFuncs := map[string]bool{}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == sinkDepthArm {
			armBody = fn
			continue
		}
		var arms, hands bool
		ast.Inspect(fn, func(n ast.Node) bool {
			switch called := callee(n).(type) {
			case *ast.SelectorExpr:
				switch called.Sel.Name {
				case sinkDepthArm:
					arms = true
				case absorptionHandoff:
					hands = true
				}
			}
			return true
		})
		if hands {
			handoffFuncs[fn.Name.Name] = arms
		}
	}

	require.NotNil(t, armBody, "%s must exist in %s", sinkDepthArm, coordinatorWiringFile)
	require.NotEmpty(t, handoffFuncs, "no engine takes the shared absorption ledger — the check has lost its subject")
	for name, arms := range handoffFuncs {
		require.True(t, arms, "%s hands out the shared absorption ledger without calling %s", name, sinkDepthArm)
	}

	var setsFromConfig bool
	ast.Inspect(armBody, func(n ast.Node) bool {
		sel, ok := callee(n).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != sinkDepthSetter {
			return true
		}
		call := n.(*ast.CallExpr)
		for _, arg := range call.Args {
			if inner, ok := callee(arg).(*ast.SelectorExpr); ok && inner.Sel.Name == sinkDepthResolver {
				setsFromConfig = true
			}
		}
		return true
	})
	require.True(t, setsFromConfig,
		"%s must call %s with %s so the prior stays operator-settable", sinkDepthArm, sinkDepthSetter, sinkDepthResolver)
}

// callee returns the function expression of a call node, or nil for anything else.
func callee(n ast.Node) ast.Expr {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil
	}
	return call.Fun
}
