package persistence_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

func declaredModelTypeNames(t *testing.T) []string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	modelsFile := filepath.Join(filepath.Dir(thisFile), "models.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, modelsFile, nil, 0)
	require.NoError(t, err)

	var names []string
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := typeSpec.Type.(*ast.StructType); !ok {
				continue
			}
			if strings.HasSuffix(typeSpec.Name.Name, "Model") {
				names = append(names, typeSpec.Name.Name)
			}
		}
	}
	return names
}

func registeredModelTypeNames() []string {
	var names []string
	for _, m := range persistence.AllModels() {
		t := reflect.TypeOf(m)
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if strings.HasSuffix(t.Name(), "Model") {
			names = append(names, t.Name())
		}
	}
	return names
}

func TestAllModelsRegistersEveryModelStruct(t *testing.T) {
	declared := declaredModelTypeNames(t)
	require.NotEmpty(t, declared, "expected to find *Model struct declarations in models.go")

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

	require.Empty(t, missing, "models declared in models.go but not registered in persistence.AllModels(): %v", missing)
}
