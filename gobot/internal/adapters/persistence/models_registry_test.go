package persistence_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

func tableNameReceiver(decl *ast.FuncDecl) (string, bool) {
	if decl.Name.Name != "TableName" || decl.Recv == nil || len(decl.Recv.List) != 1 {
		return "", false
	}
	if decl.Type.Params != nil && len(decl.Type.Params.List) != 0 {
		return "", false
	}
	results := decl.Type.Results
	if results == nil || len(results.List) != 1 || len(results.List[0].Names) > 1 {
		return "", false
	}
	if ident, ok := results.List[0].Type.(*ast.Ident); !ok || ident.Name != "string" {
		return "", false
	}
	recv := decl.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	ident, ok := recv.(*ast.Ident)
	if !ok {
		return "", false
	}
	return ident.Name, true
}

// gormTaggedModelStructs names struct types ending in "Model" that tag a field for
// gorm -- the second guard arm, since a model omitting TableName() migrates nowhere.
// The gorm tag is what separates those from absorptionRecoveryModel, a fitted market
// model that must never migrate.
func gormTaggedModelStructs(decl *ast.GenDecl) []string {
	if decl.Tok != token.TYPE {
		return nil
	}
	var names []string
	for _, spec := range decl.Specs {
		typeSpec, ok := spec.(*ast.TypeSpec)
		if !ok || !strings.HasSuffix(typeSpec.Name.Name, "Model") {
			continue
		}
		structType, ok := typeSpec.Type.(*ast.StructType)
		if !ok || structType.Fields == nil {
			continue
		}
		for _, field := range structType.Fields.List {
			if field.Tag != nil && strings.Contains(field.Tag.Value, "gorm:") {
				names = append(names, typeSpec.Name.Name)
				break
			}
		}
	}
	return names
}

func declaredModelTypeNames(t *testing.T) []string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	pkgDir := filepath.Dir(thisFile)

	fset := token.NewFileSet()
	// Test files are excluded: a fixture carrying TableName() would look like a table
	// model, and "fixing" the guard by registering it makes AutoMigrate build a real table.
	pkgs, err := parser.ParseDir(fset, pkgDir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	require.NoError(t, err)

	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if name, ok := tableNameReceiver(d); ok {
						add(name)
					}
				case *ast.GenDecl:
					for _, name := range gormTaggedModelStructs(d) {
						add(name)
					}
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

func registeredModelTypeNames() []string {
	var names []string
	for _, m := range persistence.AllModels() {
		t := reflect.TypeOf(m)
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		names = append(names, t.Name())
	}
	return names
}

func TestAllModelsRegistersEveryModelStruct(t *testing.T) {
	declared := declaredModelTypeNames(t)
	require.NotEmpty(t, declared, "expected to find persisted model types in the persistence package")

	registered := registeredModelTypeNames()

	registeredSet := make(map[string]bool, len(registered))
	for _, n := range registered {
		registeredSet[n] = true
	}

	var missing []string
	for _, d := range declared {
		if !registeredSet[d] {
			missing = append(missing, d)
		}
	}

	require.Empty(t, missing, "persisted model types not registered in persistence.AllModels(): %v", missing)
}
