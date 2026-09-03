package main

// The per-yard rescan window reaches the shipyard scanner from the [shipyard_scan] section, or
// the knob reads as configurable while nothing moves. The scanner is built in shared_readers.go
// and takes the window as a plain duration, so only the composition root can be asked.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// rescanTTLArgIndex is the position of the rescanTTL parameter in ship.NewShipyardScanner.
const rescanTTLArgIndex = 5

func TestShipyardRescanTTLIsWiredFromConfig(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	path := filepath.Join(filepath.Dir(thisFile), "shared_readers.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, "parse %s", path)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var found []ast.Expr
	ast.Inspect(file, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall || calledFuncName(call) != "NewShipyardScanner" {
			return true
		}
		require.Greater(t, len(call.Args), rescanTTLArgIndex,
			"NewShipyardScanner takes fewer arguments than expected — the index this check reads is stale")
		found = append(found, call.Args[rescanTTLArgIndex])
		return true
	})
	require.Len(t, found, 1,
		"expected exactly one NewShipyardScanner at the composition root, found %d", len(found))

	arg := string(raw[fset.Position(found[0].Pos()).Offset:fset.Position(found[0].End()).Offset])
	require.Contains(t, arg, "ResolvedRescanTTL",
		"the scanner's rescan window (%q) does not resolve through ShipyardScanConfig, so shipyard_scan.rescan_ttl_minutes cannot move it", arg)
}
