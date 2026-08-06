package commands

// ERA INVARIANCE, EXTENDED TO THE LAYERS THAT ACTUALLY RESOLVE DESTINATIONS.
//
// Waypoint and system symbols are regenerated every era. A literal in this layer works perfectly
// on today's map and then silently sends hulls nowhere the moment the era rolls — which is why it
// needs a build-failing guard rather than a comment.
//
// The gate policy package already sweeps itself (fill_test.go). That sweep globs "*.go" RELATIVE
// TO ITS OWN PACKAGE DIRECTORY, so it guards internal/domain/manufacturing/gate/ and nothing else
// — while the live executor and leg code that resolves real destinations now lives in THESE two
// packages. Those are precisely the files most likely to acquire a hard-coded waypoint.
//
// WHY THIS SCANS THE AST RATHER THAN THE FILE TEXT. The gate package's sweep is a text scan, and a
// text scan is wrong here: three production files under services/ already carry an era symbol in a
// DOC COMMENT, all three as illustrative examples —
//
//	construction_pipeline_planner.go: "The waypoint symbol of the construction site (e.g., "X1-FB5-I61")"
//	market_levels.go:                 "extracts system from waypoint (e.g., "X1-FB5-I61" -> "X1-FB5")"
//	supply_chain_resolver.go:         "SILICON_CRYSTALS has a recipe ... but no factory in X1-YZ19"
//
// None of those can send a hull anywhere, and a guard that failed the build on them would be
// deleted within a day. Only a STRING LITERAL can become a destination, so the sweep walks parsed
// string literals and comments are excluded structurally rather than by a fragile text heuristic.
//
// Goods names are the invariant and are deliberately NOT flagged: FAB_MATS and ADVANCED_CIRCUITRY
// exist in every era and are the design's anchor. Test fixtures may use literals freely — the
// guard is about production code, so *_test.go is skipped.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// eraSymbol matches an era-generated symbol: a sector prefix (letter + digits) followed by at
// least one more dash-separated chunk. It therefore catches BOTH shapes the bead names — the
// system symbol "X1-YZ19" and the waypoint symbol "X1-YZ19-A1" — because a system literal pins the
// fleet to one era exactly as hard as a waypoint does.
//
// It is applied to the DECODED value of each literal, unanchored, so an era symbol embedded in a
// format string ("jump to X1-YZ19 first") is caught too.
//
// The trailing group REPEATS so the reported match is the whole symbol rather than its first two
// chunks: a diagnostic that named "X1-FB5" when the literal is "X1-FB5-I61" invites the reader to
// go looking for the wrong string. Each chunk requires two or more characters, which keeps ordinary
// hyphenated identifiers ("H2-O") out without needing an exclusion list.
var eraSymbol = regexp.MustCompile(`[A-Z]\d+(?:-[A-Z0-9]{2,})+`)

// eraGuardedDirs are the two packages this guard covers, relative to this package's directory
// (go test runs with the package directory as its working directory).
//
// Each carries a required-file list and a floor. The list is the real non-vacuity guard: a glob
// that matched nothing — because a package moved and the relative path went stale — would
// otherwise pass while proving nothing at all.
var eraGuardedDirs = []struct {
	dir      string
	minFiles int
	required []string
}{
	{
		dir:      ".",
		minFiles: 5,
		required: []string{
			"run_construction_coordinator_gate_feed.go",
			"run_construction_coordinator_gate_realloc.go",
			"run_construction_coordinator_gate_delivery.go",
		},
	},
	{
		dir:      "../services",
		minFiles: 15,
		required: []string{
			"production_executor_gate_feed.go",
			"gate_topology.go",
		},
	},
}

// stringLiteralsOf returns every string literal in a Go source file, decoded. Comments are absent
// from the returned set by construction: parser mode 0 attaches no comments to the AST, and a
// comment is not a BasicLit in any case.
func stringLiteralsOf(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	var literals []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, uerr := strconv.Unquote(lit.Value)
		if uerr != nil {
			value = lit.Value // a shape Unquote cannot decode is still worth scanning raw
		}
		literals = append(literals, value)
		return true
	})
	return literals
}

// THE GUARD. No production string literal in either package may name an era-generated symbol.
func TestGateApplicationSources_ContainNoEraSymbolLiterals(t *testing.T) {
	// The guard must be able to fail, and calibrating on ONE example under-proves that: a
	// regression that TIGHTENS the pattern — say to [A-Z]\d+-[A-Z]{2}\d{2}-[A-Z]\d{2} — still
	// matches X1-KP23-F46, so a single-string check stays green while the guard goes blind to every
	// other shape. These are the real shapes this repo uses, plus the bare system symbol.
	for _, known := range []string{
		`x := "X1-KP23-F46"`,
		`"X1-UM5-J59"`,
		`"X1-DR-GATE"`,
		`"X1-BETA-MARKETPLACE"`,
		`"X1-YZ19"`,
	} {
		if !eraSymbol.MatchString(known) {
			t.Fatalf("era-symbol pattern failed its own calibration on %s — it cannot detect a real symbol", known)
		}
	}
	// ...and it must not fire on the invariants, or the guard would be unusable and get deleted.
	// The goods are the design's anchor and MUST remain nameable directly.
	for _, invariant := range []string{
		`good := "FAB_MATS"`,
		`good := "ADVANCED_CIRCUITRY"`,
		`inputs := []string{"IRON", "QUARTZ_SAND"}`,
		`level := "ABUNDANT"`,
		`"IRON_ORE"`,
		`"H2-O"`, // the two-or-more rule per chunk, pinned rather than asserted in a comment
	} {
		if eraSymbol.MatchString(invariant) {
			t.Fatalf("era-symbol pattern flags %s; goods and supply levels are era-invariant and must be nameable directly", invariant)
		}
	}

	for _, guarded := range eraGuardedDirs {
		sources, err := filepath.Glob(filepath.Join(guarded.dir, "*.go"))
		if err != nil {
			t.Fatalf("globbing %s: %v", guarded.dir, err)
		}
		scanned := map[string]bool{}
		for _, path := range sources {
			name := filepath.Base(path)
			if strings.HasSuffix(name, "_test.go") {
				continue // fixtures may name literals; the guard is about production code
			}
			scanned[name] = true
			for _, literal := range stringLiteralsOf(t, path) {
				if found := eraSymbol.FindAllString(literal, -1); len(found) > 0 {
					t.Fatalf("%s contains the era-generated symbol(s) %v in a string literal %q — symbols are regenerated every era, so resolve locations by market role instead. (Goods names are invariant and are always allowed.)", path, found, literal)
				}
			}
		}

		if len(scanned) < guarded.minFiles {
			t.Fatalf("guard scanned %d production file(s) in %s; it has at least %d — a sweep that reads nothing proves nothing", len(scanned), guarded.dir, guarded.minFiles)
		}
		for _, required := range guarded.required {
			if !scanned[required] {
				t.Fatalf("guard did not scan %s in %s; the sweep must cover the files that resolve real destinations, and a required file missing means the relative path went stale and the sweep is reading the wrong directory", required, guarded.dir)
			}
		}
	}
}

// PROOF THE COMMENT EXCLUSION IS STRUCTURAL, not incidental. This is the whole reason the guard
// parses instead of grepping: the three doc comments named in the header must NOT fail the build,
// while the same text as a literal must.
func TestEraSymbolGuardReadsLiteralsNotComments(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.go")
	writeGo(t, clean, `package p

// The waypoint symbol of the construction site (e.g., "X1-FB5-I61")
// extracts system from waypoint (e.g., "X1-FB5-I61" -> "X1-FB5").
// Example: SILICON_CRYSTALS has a recipe but no factory in X1-YZ19
func f() string { return "FAB_MATS" }
`)
	for _, literal := range stringLiteralsOf(t, clean) {
		if eraSymbol.MatchString(literal) {
			t.Fatalf("a doc-comment example was scanned as a literal (%q); the guard would fail the build on three pre-existing, harmless comments", literal)
		}
	}

	dirty := filepath.Join(dir, "dirty.go")
	writeGo(t, dirty, `package p

func f() string { return "X1-FB5-I61" }
`)
	var caught []string
	for _, literal := range stringLiteralsOf(t, dirty) {
		caught = append(caught, eraSymbol.FindAllString(literal, -1)...)
	}
	if len(caught) == 0 {
		t.Fatal("the SAME symbol as a real string literal was not caught; excluding comments must not have blinded the guard to code")
	}
}

// PROOF THE GUARD HAS TEETH on the shapes it exists to catch, including the ones a
// waypoint-only pattern would miss: a bare system symbol, and a symbol embedded in a format string.
func TestEraSymbolGuardCatchesEveryHardcodedShape(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, src, want string }{
		{"waypoint destination", `package p

func f() string { return "X1-FB5-I61" }
`, "X1-FB5-I61"},
		{"bare system symbol", `package p

func f() string { return "X1-YZ19" }
`, "X1-YZ19"},
		{"embedded in a format string", `package p

func f() string { return "no market for %s in X1-KP23-F46" }
`, "X1-KP23-F46"},
		{"const declaration", `package p

const homeGate = "X1-DR-GATE"
`, "X1-DR-GATE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "case.go")
			writeGo(t, path, tc.src)
			var caught []string
			for _, literal := range stringLiteralsOf(t, path) {
				caught = append(caught, eraSymbol.FindAllString(literal, -1)...)
			}
			if len(caught) == 0 {
				t.Fatalf("guard missed %s (%s); it would ship a hull-stranding literal", tc.name, tc.want)
			}
		})
	}
}

func writeGo(t *testing.T, path, src string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
