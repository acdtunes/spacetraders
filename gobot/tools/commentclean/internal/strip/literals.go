package strip

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type declRange struct {
	start, end int
	ids        map[string]bool
}

// LiteralIndex records where every bead id also appears inside a Go string
// literal. An id that a literal emits is live vocabulary -- an operator string,
// a Prometheus Help, a cobra help text, a DB-persisted reason, a test
// assertion -- and the comment that explains it must keep naming it.
type LiteralIndex struct {
	byFile  map[string]map[string]bool
	byDir   map[string]map[string]bool
	byDecl  map[string][]declRange
	nonTest map[string]bool
	all     map[string]bool
}

func BuildLiteralIndex(root string) (*LiteralIndex, error) {
	files, err := InScopeFiles(root, []string{root})
	if err != nil {
		return nil, err
	}
	idx := &LiteralIndex{
		byFile:  map[string]map[string]bool{},
		byDir:   map[string]map[string]bool{},
		byDecl:  map[string][]declRange{},
		nonTest: map[string]bool{},
		all:     map[string]bool{},
	}
	for _, abs := range files {
		src, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, abs, src, 0)
		if err != nil {
			continue // an unparseable file is reported later, not indexed
		}
		fileIDs := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, id := range IDPattern.FindAllString(lit.Value, -1) {
				fileIDs[id] = true
			}
			return true
		})
		if len(fileIDs) > 0 {
			idx.byFile[abs] = fileIDs
			dir := filepath.Dir(abs)
			if idx.byDir[dir] == nil {
				idx.byDir[dir] = map[string]bool{}
			}
			isTest := strings.HasSuffix(abs, "_test.go")
			for id := range fileIDs {
				idx.byDir[dir][id] = true
				idx.all[id] = true
				if !isTest {
					idx.nonTest[id] = true
				}
			}
		}
		var decls []declRange
		for _, d := range f.Decls {
			ids := map[string]bool{}
			ast.Inspect(d, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				for _, id := range IDPattern.FindAllString(lit.Value, -1) {
					ids[id] = true
				}
				return true
			})
			start := fset.Position(d.Pos()).Offset
			if doc := declDoc(d); doc != nil {
				start = fset.Position(doc.Pos()).Offset
			}
			decls = append(decls, declRange{start: start, end: fset.Position(d.End()).Offset, ids: ids})
		}
		idx.byDecl[abs] = decls
	}
	return idx, nil
}

func declDoc(d ast.Decl) *ast.CommentGroup {
	switch v := d.(type) {
	case *ast.FuncDecl:
		return v.Doc
	case *ast.GenDecl:
		return v.Doc
	}
	return nil
}

// For returns the literal-cohabitation set that applies to a whole file.
// Decl scope is position-dependent and served by ForDecl instead.
func (li *LiteralIndex) For(root, abs string, scope Scope) map[string]bool {
	switch scope {
	case ScopeFile:
		return li.byFile[abs]
	case ScopePkg:
		return li.byDir[filepath.Dir(abs)]
	case ScopeRepo:
		return li.all
	case ScopeDecl:
		return map[string]bool{}
	default: // hybrid
		out := map[string]bool{}
		for id := range li.byDir[filepath.Dir(abs)] {
			out[id] = true
		}
		for id := range li.nonTest {
			out[id] = true
		}
		return out
	}
}

func (li *LiteralIndex) ForDecl(abs string, offset int) map[string]bool {
	for _, d := range li.byDecl[abs] {
		if offset >= d.start && offset < d.end {
			return d.ids
		}
	}
	return map[string]bool{}
}

var excludedPrefixes = []string{
	"internal/captain/", // protected
	"pkg/proto/",        // generated
	"cmd/captain-gate/", // protected
}

var skipDirs = map[string]bool{
	".git": true, ".claude": true, "node_modules": true, "vendor": true,
}

// InScopeFiles enumerates the .go files the tool may read for modification.
// The exclusions are unconditional: they are never read, never indexed, never
// written.
func InScopeFiles(root string, paths []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(abs string) {
		if !strings.HasSuffix(abs, ".go") || seen[abs] {
			return
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "../") {
			return
		}
		for _, p := range excludedPrefixes {
			if strings.HasPrefix(rel, p) {
				return
			}
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			add(abs)
			continue
		}
		err = filepath.Walk(abs, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fi.IsDir() {
				if skipDirs[fi.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			add(path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}
