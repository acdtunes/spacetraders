package strip

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// processOne runs the whole-file pipeline against one real file with the
// repo-wide literal index built at the given scope.
func processOne(t *testing.T, root, rel string, scope Scope) (before, after []byte, res *FileResult) {
	t.Helper()
	idx := sharedIndex(t, root)
	abs := filepath.Join(root, rel)
	src, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	res, err = ProcessFile(root, abs, src, idx, Options{LiteralScope: scope, MaxPasses: 8})
	if err != nil {
		t.Fatalf("ProcessFile(%s): %v", rel, err)
	}
	if res.Rejected != "" {
		t.Fatalf("%s REJECTED: %s", rel, res.Rejected)
	}
	out := src
	if res.Changed {
		out = res.New
	}
	return src, out, res
}

var idxCache *LiteralIndex

func sharedIndex(t *testing.T, root string) *LiteralIndex {
	t.Helper()
	if idxCache == nil {
		var err error
		idxCache, err = BuildLiteralIndex(root)
		if err != nil {
			t.Fatal(err)
		}
	}
	return idxCache
}

func lineOf(b []byte, n int) string {
	ls := strings.Split(string(b), "\n")
	if n < 1 || n > len(ls) {
		return ""
	}
	return ls[n-1]
}

func assertLinesIdentical(t *testing.T, rel string, before, after []byte, from, to int) {
	t.Helper()
	for n := from; n <= to; n++ {
		if b, a := lineOf(before, n), lineOf(after, n); a != b {
			t.Errorf("%s:%d must be byte-identical\n  before: %q\n  after:  %q", rel, n, b, a)
		}
	}
}

// ---------------------------------------------------------------------------
// N01 -- THE 87-LINE CONTRACT TABLE. The mandated counter-example.
// If the design would delete or reflow this block, the design is WRONG.
// ---------------------------------------------------------------------------

func TestN01ContractTableByteIdentical(t *testing.T) {
	root := repoRoot(t)
	const rel = "internal/adapters/grpc/command_factory_registry.go"
	before, after, _ := processOne(t, root, rel, ScopeHybrid)

	assertLinesIdentical(t, rel, before, after, 272, 359)

	// Named rows from the spec, asserted individually so a failure names the row.
	for _, n := range []int{273, 318, 326, 338, 339} {
		if b, a := lineOf(before, n), lineOf(after, n); a != b {
			t.Errorf("contract table row %d changed\n  before: %q\n  after:  %q", n, b, a)
		}
	}

	// Column alignment across the whole block.
	for n := 272; n <= 359; n++ {
		if !sameRunOffsets(lineOf(before, n), lineOf(after, n)) {
			t.Errorf("contract table row %d column runs shifted", n)
		}
	}
}

// The table is protected under the NARROWEST scope that the spec permits, so
// the guarantee does not silently depend on hybrid's wider reach.
func TestN01TableHeaderRowIsPreservedUnderPackageScope(t *testing.T) {
	root := repoRoot(t)
	const rel = "internal/adapters/grpc/command_factory_registry.go"
	before, after, res := processOne(t, root, rel, ScopePkg)
	assertLinesIdentical(t, rel, before, after, 272, 359)
	for _, e := range res.Edits {
		if e.Line >= 272 && e.Line <= 359 {
			t.Errorf("rule %s fired inside the contract table at line %d: %q", e.Rule, e.Line, e.Before)
		}
	}
}

// ---------------------------------------------------------------------------
// N02 -- emitted-literal contract. The comment AND the literals it names.
// ---------------------------------------------------------------------------

func TestN02EmittedLiteralContract(t *testing.T) {
	root := repoRoot(t)
	const rel = "internal/adapters/grpc/container_ops_depot_launch.go"
	before, after, _ := processOne(t, root, rel, ScopeHybrid)
	assertLinesIdentical(t, rel, before, after, 703, 711)
	for _, want := range []string{`"sp-gvvph"`, "sp-fihvy/sp-fis8y", "sp-gvvph, RULINGS #7"} {
		if strings.Count(string(after), want) != strings.Count(string(before), want) {
			t.Errorf("literal occurrence count for %q changed", want)
		}
	}
}

// ---------------------------------------------------------------------------
// N08 -- raw backtick strings that read like comments. Any "looks like prose"
// heuristic would edit user-facing CLI output.
// ---------------------------------------------------------------------------

func TestN08BacktickLiteralsUntouched(t *testing.T) {
	root := repoRoot(t)
	for _, tc := range []struct {
		rel  string
		line int
	}{
		{"internal/adapters/cli/scout.go", 85},
		{"internal/adapters/grpc/depot_migration.go", 10},
	} {
		before, after, _ := processOne(t, root, tc.rel, ScopeHybrid)
		if b, a := lineOf(before, tc.line), lineOf(after, tc.line); a != b {
			t.Errorf("%s:%d changed\n  before: %q\n  after:  %q", tc.rel, tc.line, b, a)
		}
	}
}

// ---------------------------------------------------------------------------
// N10 -- the hybrid-scope justification. sp-difa.1 is a NON-TEST literal in a
// DIFFERENT package. This test fails under file-only or package-only scope.
// ---------------------------------------------------------------------------

func TestN10HybridScopeReachesCrossPackageNonTestLiteral(t *testing.T) {
	root := repoRoot(t)
	const rel = "internal/adapters/grpc/bootstrap_ports.go"
	idx := sharedIndex(t, root)
	abs := filepath.Join(root, rel)

	hy := idx.For(root, abs, ScopeHybrid)
	if !hy["sp-difa.1"] {
		t.Error("hybrid scope must see sp-difa.1 (non-test literal in internal/application/bootstrap/commands)")
	}
	pk := idx.For(root, abs, ScopePkg)
	if pk["sp-difa.1"] {
		t.Error("package scope must NOT see it -- otherwise this test proves nothing about hybrid")
	}

	before, after, _ := processOne(t, root, rel, ScopeHybrid)
	if b, a := lineOf(before, 271), lineOf(after, 271); a != b {
		t.Errorf("bootstrap_ports.go:271 changed under hybrid\n  before: %q\n  after:  %q", b, a)
	}
}

// ---------------------------------------------------------------------------
// T19 -- whole-corpus invariants.
// ---------------------------------------------------------------------------

func TestT19WholeCorpusInvariants(t *testing.T) {
	if testing.Short() {
		t.Skip("whole-corpus sweep")
	}
	root := repoRoot(t)
	idx := sharedIndex(t, root)
	files, err := InScopeFiles(root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 1000 {
		t.Fatalf("expected the whole gobot tree in scope, got %d files", len(files))
	}

	var changed, rejected int
	for _, abs := range files {
		src, err := os.ReadFile(abs)
		if err != nil {
			t.Fatal(err)
		}
		res, err := ProcessFile(root, abs, src, idx, Options{LiteralScope: ScopeHybrid, MaxPasses: 8})
		if err != nil {
			t.Fatalf("%s: %v", abs, err)
		}
		rel, _ := filepath.Rel(root, abs)
		if res.Rejected != "" {
			rejected++
			t.Errorf("%s REJECTED by the AST guard: %s", rel, res.Rejected)
			continue
		}
		if !res.Changed {
			continue
		}
		changed++
		out := res.New

		if got, want := bytes.Count(out, []byte("\n")), bytes.Count(src, []byte("\n")); got != want {
			t.Errorf("%s: line count %d -> %d", rel, want, got)
		}
		bl, al := strings.Split(string(src), "\n"), strings.Split(string(out), "\n")
		for i := range bl {
			if bl[i] == al[i] {
				continue
			}
			if leadWS(bl[i]) != leadWS(al[i]) {
				t.Errorf("%s:%d leading indent changed", rel, i+1)
			}
			if !sameRunOffsets(bl[i], al[i]) {
				t.Errorf("%s:%d column runs shifted\n  before: %q\n  after:  %q", rel, i+1, bl[i], al[i])
			}
			if strings.TrimRight(al[i], " \t") != al[i] {
				t.Errorf("%s:%d trailing whitespace introduced", rel, i+1)
			}
			for _, d := range DefectChecks {
				if d.Re.MatchString(al[i]) && !d.Re.MatchString(bl[i]) {
					t.Errorf("%s:%d introduced defect %s\n  before: %q\n  after:  %q", rel, i+1, d.ID, bl[i], al[i])
				}
			}
		}

		bw, aw := proseWords(src), proseWords(out)
		if lost := missingWords(bw, aw); len(lost) > 0 {
			t.Errorf("%s: prose words lost outside preWords: %v", rel, lost)
		}
		if bi, ai := literalIDBag(t, abs, src), literalIDBag(t, abs, out); !equalBags(bi, ai) {
			t.Errorf("%s: string-literal bead ids changed", rel)
		}
		nb, gb := commentNodeCounts(t, abs, src)
		na, ga := commentNodeCounts(t, abs, out)
		if nb != na || gb != ga {
			t.Errorf("%s: comment nodes %d->%d, groups %d->%d", rel, nb, na, gb, ga)
		}
	}
	if len(files) == 0 {
		t.Fatal("no files in scope -- the harness is not exercising the tool")
	}
	if changed == 0 {
		// An unchanged corpus is the EXPECTED steady state once the sweep has
		// been applied, so "changed nothing" no longer proves a broken harness
		// on its own. Discriminate the two: a dead engine and an already-clean
		// corpus are indistinguishable from the count alone, so probe the
		// engine with a known-strippable input. Strips => corpus is simply
		// clean and the sweep is idempotent. Does not strip => engine is dead.
		probe := []byte("package p\n\n// (sp-zzzz9) The probe comment.\nvar V int\n")
		pr, err := ProcessFile(root, filepath.Join(root, "probe.go"), probe, idx,
			Options{LiteralScope: ScopeHybrid, MaxPasses: 8})
		if err != nil || !pr.Changed {
			t.Fatalf("engine is dead: probe input was not stripped (err=%v) -- the harness is not exercising the tool", err)
		}
		t.Logf("corpus already stripped (%d files in scope); sweep is idempotent", len(files))
	}
	t.Logf("corpus sweep: %d files in scope, %d changed, %d rejected", len(files), changed, rejected)
}

// ---------------------------------------------------------------------------
// T20 -- gofmt parity. This catches indentation damage the AST hash cannot see.
// Implemented with go/format, never `gofmt -r` (which silently DROPS inline
// comments -- 180 -> 135 lines observed).
// ---------------------------------------------------------------------------

func TestT20GofmtParity(t *testing.T) {
	if testing.Short() {
		t.Skip("whole-corpus sweep")
	}
	root := repoRoot(t)
	idx := sharedIndex(t, root)
	files, err := InScopeFiles(root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, abs := range files {
		src, err := os.ReadFile(abs)
		if err != nil {
			t.Fatal(err)
		}
		res, err := ProcessFile(root, abs, src, idx, Options{LiteralScope: ScopeHybrid, MaxPasses: 8})
		if err != nil || res.Rejected != "" || !res.Changed {
			continue
		}
		rel, _ := filepath.Rel(root, abs)
		fb, eb := format.Source(src)
		fa, ea := format.Source(res.New)
		if (eb == nil) != (ea == nil) {
			t.Errorf("%s: gofmt-ability changed (%v -> %v)", rel, eb, ea)
			continue
		}
		if eb != nil {
			continue
		}
		if bytes.Equal(fb, src) != bytes.Equal(fa, res.New) {
			t.Errorf("%s: gofmt -l membership changed", rel)
		}
	}
}

// ---------------------------------------------------------------------------
// AST-equality gate.
// ---------------------------------------------------------------------------

func TestASTHashRejectsACodeChange(t *testing.T) {
	src := []byte("package p\n\n// sp-abcd: does a thing.\nfunc F() int { return 1 }\n")
	mangled := []byte("package p\n\n// sp-abcd: does a thing.\nfunc F() int { return 2 }\n")
	h1, err := ASTHash("a.go", src)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := ASTHash("a.go", mangled)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("ASTHash must distinguish a changed literal/expression")
	}
}

func TestASTHashIgnoresCommentsOnly(t *testing.T) {
	a := []byte("package p\n\n// sp-abcd: does a thing.\nfunc F() int { return 1 }\n")
	b := []byte("package p\n\n// Does a thing.\nfunc F() int { return 1 }\n")
	h1, _ := ASTHash("a.go", a)
	h2, _ := ASTHash("a.go", b)
	if h1 != h2 {
		t.Fatal("ASTHash must be blind to comment text")
	}
}

func TestASTHashSeesStringLiteralChange(t *testing.T) {
	a := []byte("package p\n\nvar S = \"see sp-abcd\"\n")
	b := []byte("package p\n\nvar S = \"see\"\n")
	h1, _ := ASTHash("a.go", a)
	h2, _ := ASTHash("a.go", b)
	if h1 == h2 {
		t.Fatal("mode-0 hash must also prove no string literal changed")
	}
}

// ---------------------------------------------------------------------------
// Scope wiring.
// ---------------------------------------------------------------------------

func TestExcludedPathsAreNeverInScope(t *testing.T) {
	root := repoRoot(t)
	files, err := InScopeFiles(root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, bad := range []string{"internal/captain/", "pkg/proto/", "cmd/captain-gate/"} {
			if strings.HasPrefix(rel, bad) {
				t.Errorf("excluded path in scope: %s", rel)
			}
		}
		if !strings.HasSuffix(rel, ".go") {
			t.Errorf("non-go file in scope: %s", rel)
		}
	}
	sort.Strings(files)
	t.Logf("in-scope files: %d", len(files))
}
