package main

// The opportunistic-refuel threshold reaches the route executor from config, or it does not reach it
// at all. A nil refuel strategy compiles, boots, and silently runs the executor's built-in default —
// so the knob would read as configurable while no config.yaml value could ever move it. Only a check
// against the real composition root can see that, which is why this is an AST assertion and not a
// behavioural one.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// refuelStrategyArgIndex is the position of the refuelStrategy parameter in
// ship.NewRouteExecutor's signature.
const refuelStrategyArgIndex = 5

// routeExecutorRefuelArg returns the source text of the refuelStrategy argument at the composition
// root, and whether that argument is syntactically nil.
func routeExecutorRefuelArg(t *testing.T, mainGoPath string) (src string, isNil bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainGoPath, nil, 0)
	require.NoError(t, err, "parse %s", mainGoPath)

	raw, err := os.ReadFile(mainGoPath)
	require.NoError(t, err)

	var found []ast.Expr
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || calledFuncName(call) != "NewRouteExecutor" {
			return true
		}
		require.Greater(t, len(call.Args), refuelStrategyArgIndex,
			"NewRouteExecutor takes fewer arguments than expected — the index this check reads is stale")
		found = append(found, call.Args[refuelStrategyArgIndex])
		return true
	})
	require.Len(t, found, 1,
		"expected exactly one NewRouteExecutor at the composition root, found %d — with more than one, 'the threshold is wired' stops being a single question", len(found))

	arg := resolveLocal(file, found[0])
	return string(raw[fset.Position(arg.Pos()).Offset:fset.Position(arg.End()).Offset]), isNilArg(found[0])
}

// resolveLocal follows a bare identifier argument to the expression it was assigned, so naming the
// strategy before passing it reads the same as passing it inline. Anything else is returned as-is.
func resolveLocal(file *ast.File, arg ast.Expr) ast.Expr {
	ident, ok := arg.(*ast.Ident)
	if !ok {
		return arg
	}
	resolved := arg
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		if lhs, ok := assign.Lhs[0].(*ast.Ident); ok && lhs.Name == ident.Name {
			resolved = assign.Rhs[0]
		}
		return true
	})
	return resolved
}

// THE INVARIANT, against the real composition root.
func TestRefuelThresholdIsWiredFromConfig(t *testing.T) {
	mainGoPath, _ := gatePaths(t)

	arg, isNil := routeExecutorRefuelArg(t, mainGoPath)

	require.False(t, isNil,
		"the route executor is built with a nil refuel strategy, so it substitutes its own default and the [refuel] threshold knob is dead: an operator editing config.yaml would see no change and no error")
	require.Contains(t, arg, "cfg.Refuel",
		"the route executor's refuel strategy (%q) is not derived from the [refuel] config section, so the threshold cannot be retuned without a rebuild", arg)
}
