package persistence_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

var writeMethodNames = map[string]bool{"Delete": true, "Update": true, "Updates": true}

var scopingPredicatePattern = regexp.MustCompile(`(?i)player_id|(^|_)id\s*=|(^|_)id\s+in\b`)

func archiveClassRepositoryFilesScopedByThisGuard_ExcludesWipeAndOperationalJunkTables() []string {
	return []string{
		"transaction_repository.go",
		"contract_repository.go",
		"market_price_history_repository.go",
		"captain_event_repository.go",
		"manufacturing_pipeline_repository.go",
		"manufacturing_task_repository.go",
		"goods_factory_repository.go",
	}
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

func findUnscopedWrites(t *testing.T, file string) []writeViolation {
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

	ast.Inspect(astFile, func(n ast.Node) bool {
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

	return violations
}

func TestArchiveClassRepositoryWritesCarryAPlayerOrIDScopedPredicate_HeuristicBlindToCrossStatementQueryBuildersAndHelperIndirection(t *testing.T) {
	dir := persistenceDir(t)

	var all []writeViolation
	for _, name := range archiveClassRepositoryFilesScopedByThisGuard_ExcludesWipeAndOperationalJunkTables() {
		all = append(all, findUnscopedWrites(t, filepath.Join(dir, name))...)
	}

	require.Empty(t, all, "unscoped write(s) found: %+v", all)
}
