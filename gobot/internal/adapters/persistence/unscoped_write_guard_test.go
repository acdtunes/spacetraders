package persistence_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var writeMethodNames = map[string]bool{"Delete": true, "Update": true, "Updates": true}

var scopingPredicatePattern = regexp.MustCompile(`(?i)player_id|(^|_)id\s*=|(^|_)id\s+in\b`)

// Keyed on receiver type, not filename: a method moved to another file in this
// package stays guarded, where a filename list would drop it silently.
func archiveClassRepositoryTypesScopedByThisGuard_ExcludesWipeAndOperationalJunkTables() []string {
	return []string{
		"GormTransactionRepository",
		"GormContractRepository",
		"GormMarketPriceHistoryRepository",
		"GormCaptainEventRepository",
		"GormManufacturingPipelineRepository",
		"GormManufacturingTaskRepository",
	}
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func packageGoFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	var out []string
	for _, f := range entries {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	require.NotEmpty(t, out, "no production files found in %s", dir)
	return out
}

func persistenceDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Dir(thisFile)
}

func isSaveCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Save"
}

func isLoadedEntityModelCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Model" || len(call.Args) != 1 {
		return false
	}
	unary, ok := call.Args[0].(*ast.UnaryExpr)
	if !ok || unary.Op != token.AND {
		return false
	}
	_, isIdent := unary.X.(*ast.Ident)
	return isIdent
}

func statementHasScopingPredicate(stmt ast.Node) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isSaveCall(call) || isLoadedEntityModelCall(call) {
			found = true
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Where" || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if scopingPredicatePattern.MatchString(value) {
			found = true
		}
		return true
	})
	return found
}

type writeViolation struct {
	file string
	line int
	text string
}

func findUnscopedWrites(t *testing.T, file string, guarded map[string]bool, found map[string]bool) []writeViolation {
	t.Helper()

	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, file, nil, 0)
	require.NoError(t, err)

	var violations []writeViolation
	seen := map[token.Pos]bool{}

	analyzeUnit := func(unit ast.Node) {
		var writeCall *ast.CallExpr
		ast.Inspect(unit, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !writeMethodNames[sel.Sel.Name] {
				return true
			}
			writeCall = call
			return true
		})
		if writeCall == nil || seen[writeCall.Pos()] {
			return
		}
		seen[writeCall.Pos()] = true
		if !statementHasScopingPredicate(unit) {
			pos := fset.Position(writeCall.Pos())
			violations = append(violations, writeViolation{
				file: filepath.Base(file),
				line: pos.Line,
				text: "write call without a player/id-scoped predicate in its statement",
			})
		}
	}

	for _, decl := range astFile.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		recv := receiverTypeName(fn)
		if !guarded[recv] {
			continue
		}
		found[recv] = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.AssignStmt:
				analyzeUnit(stmt)
			case *ast.ExprStmt:
				analyzeUnit(stmt)
			case *ast.IfStmt:
				if stmt.Init != nil {
					analyzeUnit(stmt.Init)
				}
			}
			return true
		})
	}

	return violations
}

func TestArchiveClassRepositoryWritesCarryAPlayerOrIDScopedPredicate_HeuristicBlindToCrossStatementQueryBuildersAndHelperIndirection(t *testing.T) {
	dir := persistenceDir(t)

	guarded := map[string]bool{}
	for _, name := range archiveClassRepositoryTypesScopedByThisGuard_ExcludesWipeAndOperationalJunkTables() {
		guarded[name] = true
	}

	found := map[string]bool{}
	var all []writeViolation
	for _, file := range packageGoFiles(t, dir) {
		all = append(all, findUnscopedWrites(t, file, guarded, found)...)
	}

	// A guarded type that no longer exists means the list is stale, not that the
	// package is clean; without this the guard would silently cover nothing.
	for name := range guarded {
		require.True(t, found[name], "guarded type %s declares no methods in this package - renamed or removed?", name)
	}

	require.Empty(t, all, "unscoped write(s) found: %+v", all)
}
