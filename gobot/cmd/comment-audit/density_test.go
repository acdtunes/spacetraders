package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDocCreditCoversExportedGodocUpToTheAllowance(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\n")
	for i := 0; i < DocAllowance+4; i++ {
		b.WriteString("// line\n")
	}
	b.WriteString("func Exported() {}\n")

	st := mustAnalyze(t, b.String())
	if st.Doc != DocAllowance {
		t.Fatalf("Doc = %d, want %d: a doc block is credited only up to the allowance",
			st.Doc, DocAllowance)
	}
	if st.ProseLines() != 4 {
		t.Fatalf("ProseLines = %d, want 4: the overflow past the allowance is prose", st.ProseLines())
	}
}

func TestDocCreditIgnoresUnexportedDeclarations(t *testing.T) {
	src := `package p

// one
// two
func unexported() {}
`
	st := mustAnalyze(t, src)
	if st.Doc != 0 {
		t.Fatalf("Doc = %d, want 0: a comment on an unexported helper is not published API", st.Doc)
	}
	if st.ProseLines() != 2 {
		t.Fatalf("ProseLines = %d, want 2", st.ProseLines())
	}
}

// A doc.go is nothing but a package doc, so a bounded credit would condemn the
// file the convention asks for.
func TestPackageDocIsCreditedWhole(t *testing.T) {
	var b strings.Builder
	for i := 0; i < DocAllowance*4; i++ {
		b.WriteString("// front page\n")
	}
	b.WriteString("package p\n")

	st := mustAnalyze(t, b.String())
	if st.ProseLines() != 0 {
		t.Fatalf("ProseLines = %d, want 0 for a doc.go (Doc=%d of Comment=%d)",
			st.ProseLines(), st.Doc, st.Comment)
	}
}

func TestDocCreditCoversExportedFieldsAndBlockMembers(t *testing.T) {
	src := `package p

// T is a type.
type T struct {
	// Exported is a field.
	Exported int
	// hidden is not.
	hidden int
}

const (
	// A is a constant.
	A = 1
)
`
	st := mustAnalyze(t, src)
	if st.ProseLines() != 1 {
		t.Fatalf("ProseLines = %d, want 1 (only the unexported field's comment): Doc=%d Comment=%d",
			st.ProseLines(), st.Doc, st.Comment)
	}
}

// A declaration block's doc sits on the GenDecl, not on the spec, so a block
// holding any exported name has to count as exported.
func TestDocCreditCoversABlockDocWhenAnySpecIsExported(t *testing.T) {
	src := `package p

// Doc for the block.
var (
	unexportedFirst = 1
	Exported        = 2
)
`
	st := mustAnalyze(t, src)
	if st.Doc == 0 {
		t.Fatalf("Doc = 0: the block doc was not credited (Comment=%d)", st.Comment)
	}
}

func TestProseBlocksNameTheLongestRunFirst(t *testing.T) {
	src := `package p

// short
func a() {}

// long one
// long two
// long three
func b() {}
`
	st := mustAnalyze(t, src)
	if len(st.Blocks) != 2 {
		t.Fatalf("Blocks = %+v, want 2 runs", st.Blocks)
	}
	if st.Blocks[0].Lines != 3 {
		t.Fatalf("Blocks[0] = %+v, want the 3-line run first", st.Blocks[0])
	}
	if st.Blocks[0].Line != 6 {
		t.Fatalf("Blocks[0].Line = %d, want 6", st.Blocks[0].Line)
	}
}

func TestCheckFlagsAFileOverTheProseCeiling(t *testing.T) {
	pkgs := map[string]*PkgStat{
		"p": {Package: "p", Comment: 10, Total: 100, FileStats: []*FileStat{
			{Path: "p/dense.go", Total: 10, Comment: 8, Doc: 1},
			{Path: "p/documented.go", Total: 10, Comment: 8, Doc: 8},
		}},
	}
	got := Check(pkgs, CheckOpts{MaxMarkers: -1, MaxFileProseRatio: DefaultMaxFileProseRatio})
	if len(got) != 1 {
		t.Fatalf("violations = %v, want only the dense file", got)
	}
	if got[0].File != "p/dense.go" {
		t.Fatalf("flagged %q, want p/dense.go", got[0].File)
	}
	if !strings.Contains(got[0].String(), "p/dense.go") {
		t.Fatalf("the message names no file: %s", got[0])
	}
}

// The ceiling is what the ratchet cannot do: a package under its baseline still
// fails when one file inside it is mostly prose.
func TestCheckFlagsADenseFileInsideAPackageUnderItsBaseline(t *testing.T) {
	pkgs := map[string]*PkgStat{
		"p": {Package: "p", Comment: 100, Total: 1000, FileStats: []*FileStat{
			{Path: "p/essay.go", Total: 60, Comment: 50, Doc: 5},
		}},
	}
	bl := &Baseline{Packages: map[string]BaselineEntry{
		"p": {Ratio: 0.50, Comment: 500, Total: 1000},
	}}
	got := Check(pkgs, GatePolicy(bl, ExactPackages(nil)))
	if len(got) != 1 || got[0].File != "p/essay.go" {
		t.Fatalf("violations = %v, want the file alone (the package is well under baseline)", got)
	}
}

func TestGatePolicyCarriesTheCeilingSoCallersCannotDrift(t *testing.T) {
	opts := GatePolicy(nil, ExactPackages(nil))
	if opts.MaxFileProseRatio != DefaultMaxFileProseRatio {
		t.Fatalf("MaxFileProseRatio = %v, want %v", opts.MaxFileProseRatio, DefaultMaxFileProseRatio)
	}
	if opts.Tolerance != 0 {
		t.Fatalf("Tolerance = %v, want 0: the ratchet is strict", opts.Tolerance)
	}
}

// The advice is only useful if cutting exactly that many lines clears the file.
func TestLinesToCutClearsTheCeiling(t *testing.T) {
	cases := []struct{ prose, total int }{
		{8, 10}, {50, 60}, {147, 293}, {15, 25}, {200, 313}, {51, 100},
	}
	for _, c := range cases {
		k := LinesToCut(c.prose, c.total, DefaultMaxFileProseRatio)
		after := FileStat{Total: c.total - k, Comment: c.prose - k}
		if after.ProseRatio() > DefaultMaxFileProseRatio {
			t.Errorf("prose=%d total=%d: cutting %d leaves %.4f, still over %.2f",
				c.prose, c.total, k, after.ProseRatio(), DefaultMaxFileProseRatio)
		}
		if k > 0 {
			less := FileStat{Total: c.total - (k - 1), Comment: c.prose - (k - 1)}
			if less.ProseRatio() <= DefaultMaxFileProseRatio {
				t.Errorf("prose=%d total=%d: %d lines is more than needed", c.prose, c.total, k)
			}
		}
	}
	if got := LinesToCut(1, 10, DefaultMaxFileProseRatio); got != 0 {
		t.Fatalf("LinesToCut on a compliant file = %d, want 0", got)
	}
	if got := LinesToCut(1, 0, DefaultMaxFileProseRatio); got != 0 {
		t.Fatalf("LinesToCut on an empty file = %d, want 0", got)
	}
}

func TestScanRecordsFilePathsRelativeToRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/one.go", "package a\n\n// c\nfunc A() {}\n")

	pkgs, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	p := pkgs["a"]
	if p == nil || len(p.FileStats) != 1 {
		t.Fatalf("packages = %v, want one file under a", keysOf(pkgs))
	}
	if p.FileStats[0].Path != "a/one.go" {
		t.Fatalf("Path = %q, want a/one.go: violations would name an unopenable path",
			p.FileStats[0].Path)
	}
}
