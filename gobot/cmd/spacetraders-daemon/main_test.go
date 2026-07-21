package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file is the gate-hardening check for a defect class where a Command/Query
// type has a real handler, a real domain implementation, and passing unit tests
// (which call the handler directly) but no mediator.RegisterHandler call in
// main.go, so every real dispatch through the mediator fails with "no handler
// registered for type ...". Unit tests exercising a handler in isolation can
// never catch this class; only a check of the composition root itself can.
//
// The check parses cmd/spacetraders-daemon/main.go - the sole production
// composition root (internal/application/setup/handler_registry.go is a
// partial/dead registry: it wires only 8 of ~50 real handlers and has no
// caller besides a passing reference in internal/adapters/cli/ship.go, so it
// is NOT ground truth) - for every mediator.RegisterHandler[T] call, and
// every internal/application/**/*.go file for every declared
// `type XCommand/XQuery struct`. Every declared type must appear in the
// registered set, unless explicitly exempted in knownUnregisteredExceptions
// with a documented reason.

// knownUnregisteredExceptions lists every declared Command/Query type that is
// legitimately NOT registered via the mediator, with the reason it is safe.
// Adding an entry here silently bypasses the gate for that type - use it only
// for a confirmed non-mediator dispatch path or genuinely dead code (verified
// by grep: no construction site anywhere), never as a shortcut to unblock a
// merge. TestKnownUnregisteredExceptionsAreStillAccurate keeps this list from
// rotting: an entry for a type that either disappears or becomes registered
// fails that test.
var knownUnregisteredExceptions = map[string]string{
	"RunFactoryWorkerCommand":  "type declared (internal/application/manufacturing/types/factory_types.go) with no handler implementation anywhere in the codebase; dead/aspirational code predating sp-423c, superseded by RunFactoryCoordinatorCommand",
	"RefreshMarketDataCommand": "RefreshMarketDataHandler exists (internal/application/scouting/commands/refresh_market_data.go) but nothing constructs or dispatches this command anywhere in the codebase; dead code predating sp-423c",
	"SyncPlayerCommand":        "handler exists (internal/application/player/commands/register_player.go) but nothing constructs or dispatches this command anywhere in the codebase, unlike its sibling RegisterPlayerCommand; dead code predating sp-423c",
	"RegisterPlayerCommand":    "dispatched via a direct handler.Handle() call from the CLI (internal/adapters/cli/player.go), bypassing the mediator by design",
	"CargoTransactionCommand":  "dispatched via a direct handler.Handle() call from SellCargoHandler/PurchaseCargoHandler as an internal shared-handler composition (internal/application/ship/commands/cargo/), bypassing the mediator by design",
}

// TestEveryDeclaredCommandAndQueryIsRegisteredOrExempt is the primary gate: a
// declared Command/Query type that is neither registered in main.go nor
// listed in knownUnregisteredExceptions means dispatching it through the
// mediator would fail with "no handler registered for type ..." in
// production.
func TestEveryDeclaredCommandAndQueryIsRegisteredOrExempt(t *testing.T) {
	mainGoPath, appDir := gatePaths(t)

	registered := registeredHandlerTypes(t, mainGoPath)
	declared := declaredCommandAndQueryTypes(t, appDir)

	for name, locations := range declared {
		if len(locations) > 1 {
			t.Errorf("type name %q is declared in multiple files (%v); this check matches by short name only and requires globally-unique Command/Query names", name, locations)
		}
	}

	for name, locations := range declared {
		if _, ok := registered[name]; ok {
			continue
		}
		if _, ok := knownUnregisteredExceptions[name]; ok {
			continue
		}
		t.Errorf("%s (%v) is declared under internal/application but never registered via mediator.RegisterHandler in cmd/spacetraders-daemon/main.go; "+
			"either register it there, or add it to knownUnregisteredExceptions with a documented reason. "+
			"sp-n0x7 shipped exactly this gap (JumpShipCommand had a handler but was never registered), and every real dispatch failed with \"no handler registered for type\"",
			name, locations)
	}
}

// TestKnownUnregisteredExceptionsAreStillAccurate guards the exception list
// itself against rot: an entry for a type that no longer exists, or that has
// since been registered, must be removed - otherwise the list would keep
// silently exempting fewer and fewer real gaps as it drifts, or worse, mask a
// grep mistake made when the entry was added.
func TestKnownUnregisteredExceptionsAreStillAccurate(t *testing.T) {
	mainGoPath, appDir := gatePaths(t)

	registered := registeredHandlerTypes(t, mainGoPath)
	declared := declaredCommandAndQueryTypes(t, appDir)

	for name, reason := range knownUnregisteredExceptions {
		if _, ok := declared[name]; !ok {
			t.Errorf("knownUnregisteredExceptions lists %q (%s) but no such type is declared anymore under internal/application; remove the stale entry", name, reason)
			continue
		}
		if _, ok := registered[name]; ok {
			t.Errorf("knownUnregisteredExceptions lists %q (%s) but it IS now registered in main.go; remove the stale entry", name, reason)
		}
	}
}

// TestRegistrationGapDetectionHasTeeth proves the diff logic itself actually
// flags a missing registration, so a green primary test means "everything is
// wired," not "the parser silently matched nothing." It seeds a
// declared-but-unregistered, non-exempt type and asserts the gap is caught.
func TestRegistrationGapDetectionHasTeeth(t *testing.T) {
	registered := map[string]struct{}{"RegisteredCommand": {}}
	declared := map[string][]string{
		"RegisteredCommand":   {"pkg/a/registered.go"},
		"UnregisteredCommand": {"pkg/b/unregistered.go"}, // deliberately missing
	}

	gaps := unregisteredGaps(registered, declared, knownUnregisteredExceptions)

	require.Equal(t, []string{"UnregisteredCommand"}, gaps,
		"detection must flag a declared-but-unregistered, non-exempt type and nothing else")
}

// TestRegistrationGapDetectionRespectsExceptions proves the same logic does
// NOT flag a type that is both unregistered and explicitly exempted -
// confirming the exception list actually silences the gate rather than being
// dead configuration the checker ignores.
func TestRegistrationGapDetectionRespectsExceptions(t *testing.T) {
	registered := map[string]struct{}{}
	declared := map[string][]string{"ExemptCommand": {"pkg/a/exempt.go"}}
	exceptions := map[string]string{"ExemptCommand": "test double for the exception path"}

	gaps := unregisteredGaps(registered, declared, exceptions)

	require.Empty(t, gaps, "an explicitly-exempted type must not be reported as a gap")
}

// unregisteredGaps returns every declared name that is neither registered nor
// exempt, sorted for deterministic assertions. Extracted from the primary
// test so TestRegistrationGapDetectionHasTeeth can exercise it directly
// against synthetic input, without touching the filesystem or real main.go.
func unregisteredGaps(registered map[string]struct{}, declared map[string][]string, exceptions map[string]string) []string {
	var gaps []string
	for name := range declared {
		if _, ok := registered[name]; ok {
			continue
		}
		if _, ok := exceptions[name]; ok {
			continue
		}
		gaps = append(gaps, name)
	}
	sortStrings(gaps)
	return gaps
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// gatePaths resolves main.go and the internal/application tree relative to
// this test file's own location, so the check is independent of the working
// directory `go test` runs in (mirrors migrationsDir in
// internal/adapters/persistence/schema_enum_drift_test.go).
func gatePaths(t *testing.T) (mainGoPath, appDir string) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// thisFile = <gobot>/cmd/spacetraders-daemon/main_test.go
	dir := filepath.Dir(thisFile)
	mainGoPath = filepath.Join(dir, "main.go")
	appDir = filepath.Join(dir, "..", "..", "internal", "application")
	if _, err := os.Stat(mainGoPath); err != nil {
		t.Fatalf("main.go not found at %s: %v", mainGoPath, err)
	}
	if _, err := os.Stat(appDir); err != nil {
		t.Fatalf("internal/application not found at %s: %v", appDir, err)
	}
	return mainGoPath, appDir
}

// registeredHandlerTypes parses mainGoPath and returns the set of type names
// passed as the sole type argument to every mediator.RegisterHandler[T] call,
// e.g. mediator.RegisterHandler[*shipNav.JumpShipCommand](med, handler) yields
// "JumpShipCommand". Matching is by short type name only, package-qualifier
// and pointer stripped - safe because Command/Query type names are unique
// across the codebase (enforced by the uniqueness check in the primary test).
func registeredHandlerTypes(t *testing.T, mainGoPath string) map[string]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainGoPath, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", mainGoPath, err)
	}

	registered := make(map[string]struct{})
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// mediator.RegisterHandler[T](...) with a single explicit type
		// argument parses as an IndexExpr (Go 1.18+ generics); multiple type
		// arguments would be an IndexListExpr, but RegisterHandler has only
		// one type parameter so every real call site is an IndexExpr.
		index, ok := call.Fun.(*ast.IndexExpr)
		if !ok {
			return true
		}
		sel, ok := index.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "RegisterHandler" {
			return true
		}
		if pkgIdent, ok := sel.X.(*ast.Ident); !ok || pkgIdent.Name != "mediator" {
			return true
		}
		if name := typeArgName(index.Index); name != "" {
			registered[name] = struct{}{}
		}
		return true
	})
	if len(registered) == 0 {
		t.Fatalf("parsed zero mediator.RegisterHandler[...] calls from %s; parser or main.go shape changed", mainGoPath)
	}
	return registered
}

// typeArgName reduces a type expression (e.g. *shipNav.JumpShipCommand) to
// its bare type name (JumpShipCommand), stripping the pointer and package
// qualifier.
func typeArgName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return typeArgName(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.Ident:
		return e.Name
	default:
		return ""
	}
}

// declaredCommandAndQueryTypes walks appDir for every exported
// `type XCommand struct{...}` / `type XQuery struct{...}` declaration, keyed
// by short type name and mapping to the declaring file(s) (used for
// diagnostics and the cross-package name-uniqueness check).
func declaredCommandAndQueryTypes(t *testing.T, appDir string) map[string][]string {
	t.Helper()

	declared := make(map[string][]string)
	fset := token.NewFileSet()
	err := filepath.WalkDir(appDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, ok := ts.Type.(*ast.StructType); !ok {
					continue
				}
				name := ts.Name.Name
				if !ast.IsExported(name) {
					continue
				}
				if strings.HasSuffix(name, "Command") || strings.HasSuffix(name, "Query") {
					declared[name] = append(declared[name], path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", appDir, err)
	}
	if len(declared) == 0 {
		t.Fatalf("discovered zero Command/Query types under %s; parser or directory shape changed", appDir)
	}
	return declared
}
