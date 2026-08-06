package main

// THE CALL-SITE CHECK, DELIBERATELY STANDALONE. It references nothing from the daemon package, so
// it can be run against ANY revision of main.go — including the one that panicked staging. A check
// that only fails by not compiling against the broken revision would prove the refactor is absent,
// not that the defect is caught.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// noteReplayArgs flags a panicking player-id constructor inside the replay's own call — the defect:
// it turns an unconfigured player id into a dead daemon.
func noteReplayArgs(call *ast.CallExpr, found *bool) {
	ast.Inspect(call, func(inner ast.Node) bool {
		if sel, ok := inner.(*ast.SelectorExpr); ok && sel.Sel.Name == "MustNewPlayerID" {
			*found = true
		}
		return true
	})
}

// THE COMPOSITION ROOT IS WHERE THIS BROKE, so it is where it is pinned. Nothing below main.go can
// see any of the three properties below, and the panicking version satisfied every unit test.
func TestMainWiring_ReplayIsSafeAndCorrectlyOrdered(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err)

	var replayPos, ledgerPos, accrualHandoffPos token.Pos
	var panickingIDInReplay bool

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "replayLaneCooldown" {
				replayPos = call.Pos()
				noteReplayArgs(call, &panickingIDInReplay)
			}
		case *ast.SelectorExpr:
			switch fn.Sel.Name {
			// Either shape counts as the replay call site: the helper, or Rebuild wired inline.
			// Matching both is what makes this a check on the DEFECT rather than on the refactor
			// that fixed it — it fails against the panicking version instead of merely not
			// compiling against it.
			case "Rebuild":
				replayPos = call.Pos()
				noteReplayArgs(call, &panickingIDInReplay)
			case "NewLaneCooldownLedger":
				ledgerPos = call.Pos()
			case "SetSourceCooldown":
				// The seam that hands the ledger to an engine that accrues into it.
				if accrualHandoffPos == token.NoPos {
					accrualHandoffPos = call.Pos()
				}
			}
		}
		return true
	})

	require.NotEqual(t, token.NoPos, replayPos, "the replay must be wired at the composition root, or the ledger boots amnesiac")
	require.False(t, panickingIDInReplay,
		"the boot replay must read the configured player id through the error-returning constructor: it is zero on any captain-less deployment, and the Must- form panics before the daemon listens")

	require.NotEqual(t, token.NoPos, ledgerPos)
	require.Less(t, int(ledgerPos), int(replayPos), "the replay must run after the ledger it fills exists")

	require.NotEqual(t, token.NoPos, accrualHandoffPos, "no engine is handed the ledger, so nothing accrues into it")
	require.Less(t, int(replayPos), int(accrualHandoffPos),
		"the replay must run BEFORE the ledger reaches an engine that accrues: Rebuild refuses a key already carrying debt, so a later replay silently restores nothing")
}
