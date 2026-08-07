package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Marker is an archaeology pattern: prose whose truth depends on the file's history
// rather than on the code as it stands (ENGINEERING.md §6).
type Marker struct {
	Name string
	Re   *regexp.Regexp
}

// Markers match comment text only, never code; advisory unless -max-markers is set.
var Markers = []Marker{
	{"bead-id", regexp.MustCompile(`\bsp-[a-z0-9]{4}`)},
	{"measured live", regexp.MustCompile(`(?i)measured live`)},
	{"the defect", regexp.MustCompile(`(?i)the defect`)},
	{"used to", regexp.MustCompile(`(?i)\bused to\b`)},
	{"this fixed", regexp.MustCompile(`(?i)this fixed`)},
	{"previously", regexp.MustCompile(`(?i)\bpreviously\b`)},
	{"was found", regexp.MustCompile(`(?i)\bwas found\b`)},
}

// generatedRe excludes generated files: their density is nobody's to edit.
var generatedRe = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// MarkerHit locates one archaeology marker for the -explain report.
type MarkerHit struct {
	Marker string `json:"marker"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
}

// ProseBlock is one unbroken run of uncredited comment, so a violation can name where the bulk sits.
type ProseBlock struct {
	Line  int `json:"line"`
	Lines int `json:"lines"`
}

// FileStat is the line census of one source file. Comment+Blank+Code == Total,
// and Doc is the subset of Comment the doc credit covers.
type FileStat struct {
	Path    string         `json:"path"`
	Total   int            `json:"total"`
	Comment int            `json:"comment"`
	Doc     int            `json:"doc"`
	Blank   int            `json:"blank"`
	Code    int            `json:"code"`
	Markers map[string]int `json:"markers,omitempty"`
	Blocks  []ProseBlock   `json:"prose_blocks,omitempty"`
	Hits    []MarkerHit    `json:"-"`
}

// Ratio is the comment share of ALL lines; blanks sit in the denominator so it is stable under reflowing.
func (f FileStat) Ratio() float64 {
	if f.Total == 0 {
		return 0
	}
	return float64(f.Comment) / float64(f.Total)
}

// ProseLines is the comment lines the doc credit did not cover.
func (f FileStat) ProseLines() int { return f.Comment - f.Doc }

// ProseRatio is the share of the file that is comment beyond the doc credit,
// subtracted because convention REQUIRES godoc on exported API. It is a CREDIT
// and not an exemption: it runs out at DocAllowance lines per declaration, so a
// long doc block counts past the cap even though `go doc` prints all of it, and
// documentation alone can put a file over the ceiling.
func (f FileStat) ProseRatio() float64 {
	if f.Total == 0 {
		return 0
	}
	return float64(f.ProseLines()) / float64(f.Total)
}

// PkgStat aggregates a directory's non-test, non-generated files.
type PkgStat struct {
	Package   string         `json:"package"`
	Files     int            `json:"files"`
	Total     int            `json:"total"`
	Comment   int            `json:"comment"`
	Doc       int            `json:"doc"`
	Blank     int            `json:"blank"`
	Code      int            `json:"code"`
	Markers   map[string]int `json:"markers,omitempty"`
	FileStats []*FileStat    `json:"file_stats,omitempty"`
	Hits      []MarkerHit    `json:"-"`
}

// Ratio is the package's comment share of all lines.
func (p PkgStat) Ratio() float64 {
	if p.Total == 0 {
		return 0
	}
	return float64(p.Comment) / float64(p.Total)
}

// MarkerTotal is the number of archaeology matches across the package.
func (p PkgStat) MarkerTotal() int {
	n := 0
	for _, c := range p.Markers {
		n += c
	}
	return n
}

// AnalyzeFile censuses one file's lines, reporting (nil, nil) for a file
// excluded by policy. src must parse as Go: a comment census built by regex is
// fooled by "//" inside a string literal, so the extents come from the parser.
func AnalyzeFile(path string, src []byte) (*FileStat, error) {
	lines := splitLines(src)
	for i, l := range lines {
		if i >= 10 {
			break
		}
		if generatedRe.MatchString(strings.TrimRight(l, "\r")) {
			return nil, nil
		}
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	isComment := make(map[int]bool)
	isDoc := docLines(fset, f)
	st := &FileStat{Path: path, Total: len(lines), Markers: map[string]int{}}

	for _, cg := range f.Comments {
		for _, c := range cg.List {
			start := fset.Position(c.Pos()).Line
			end := fset.Position(c.End()).Line
			for l := start; l <= end; l++ {
				isComment[l] = true
			}
			for _, m := range Markers {
				body := c.Text
				for _, loc := range m.Re.FindAllStringIndex(body, -1) {
					st.Markers[m.Name]++
					st.Hits = append(st.Hits, MarkerHit{
						Marker: m.Name,
						File:   path,
						Line:   start + strings.Count(body[:loc[0]], "\n"),
						Text:   snippet(body, loc[0]),
					})
				}
			}
		}
	}

	run := 0
	for i, l := range lines {
		lineNo := i + 1
		prose := false
		switch {
		case isComment[lineNo]:
			st.Comment++
			if isDoc[lineNo] {
				st.Doc++
			} else {
				prose = true
			}
		case strings.TrimSpace(l) == "":
			st.Blank++
		default:
			st.Code++
		}
		if prose {
			run++
			continue
		}
		if run > 0 {
			st.Blocks = append(st.Blocks, ProseBlock{Line: lineNo - run, Lines: run})
			run = 0
		}
	}
	if run > 0 {
		st.Blocks = append(st.Blocks, ProseBlock{Line: len(lines) + 1 - run, Lines: run})
	}
	sort.SliceStable(st.Blocks, func(i, j int) bool { return st.Blocks[i].Lines > st.Blocks[j].Lines })
	if len(st.Markers) == 0 {
		st.Markers = nil
	}
	return st, nil
}

// DocAllowance is how many lines of one exported declaration's godoc the
// per-file ceiling forgives. Unbounded it would be no credit at all: any essay
// clears the ceiling from a doc position above an exported name. It covers an
// ordinary block, so what it declines to forgive is length, not documentation.
const DocAllowance = 5

// docLines marks the lines the doc credit covers: godoc on exported API, at most
// DocAllowance lines of any one block. Exportedness is the discriminator because
// the convention obliges that comment; the cap applies anyway, an obliged comment
// being no more unlimited than any other. A declaration block counts as exported
// when any one of its specs is: one comment covers the block.
func docLines(fset *token.FileSet, f *ast.File) map[int]bool {
	marked := map[int]bool{}
	// A negative budget is unbounded.
	markN := func(cg *ast.CommentGroup, budget int) {
		if cg == nil {
			return
		}
		for _, c := range cg.List {
			for l, end := fset.Position(c.Pos()).Line, fset.Position(c.End()).Line; l <= end; l++ {
				if budget == 0 {
					return
				}
				marked[l] = true
				budget--
			}
		}
	}
	mark := func(cg *ast.CommentGroup) { markN(cg, DocAllowance) }

	// The package doc is credited whole: it is the package's front page, not one
	// symbol's godoc, and a doc.go holds nothing else.
	markN(f.Doc, -1)
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name.IsExported() {
				mark(d.Doc)
			}
		case *ast.GenDecl:
			exported := false
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						exported = true
						mark(s.Doc)
						mark(s.Comment)
						markExportedFields(s.Type, mark)
					}
				case *ast.ValueSpec:
					if anyExported(s.Names) {
						exported = true
						mark(s.Doc)
						mark(s.Comment)
					}
				}
			}
			if exported {
				mark(d.Doc)
			}
		}
	}
	return marked
}

// markExportedFields marks field docs inside an exported struct or interface. An
// embedded field has no name of its own and is always published.
func markExportedFields(t ast.Expr, mark func(*ast.CommentGroup)) {
	var fields *ast.FieldList
	switch ty := t.(type) {
	case *ast.StructType:
		fields = ty.Fields
	case *ast.InterfaceType:
		fields = ty.Methods
	default:
		return
	}
	if fields == nil {
		return
	}
	for _, f := range fields.List {
		if len(f.Names) == 0 || anyExported(f.Names) {
			mark(f.Doc)
			mark(f.Comment)
		}
	}
}

func anyExported(names []*ast.Ident) bool {
	for _, n := range names {
		if n.IsExported() {
			return true
		}
	}
	return false
}

// snippet returns the marker's own line, trimmed, for the -explain report.
func snippet(body string, off int) string {
	start := strings.LastIndexByte(body[:off], '\n') + 1
	end := strings.IndexByte(body[off:], '\n')
	if end < 0 {
		end = len(body)
	} else {
		end += off
	}
	s := strings.TrimSpace(body[start:end])
	if len(s) > 100 {
		s = s[:100] + "…"
	}
	return s
}

// splitLines counts a trailing newline as a terminator, not as an extra line.
func splitLines(src []byte) []string {
	s := string(src)
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// skipDirs are never walked: vendor and testdata are not ours to police, node_modules
// and .git are noise, and .claude holds sibling worktrees that would be censused as this one.
var skipDirs = map[string]bool{
	"vendor": true, "testdata": true, "node_modules": true,
	".git": true, ".claude": true,
}

// Scan walks root and returns one PkgStat per directory holding non-test Go
// files, keyed by slash-separated path relative to root.
func Scan(root string) (map[string]*PkgStat, error) {
	pkgs := map[string]*PkgStat{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		st, err := AnalyzeFile(path, src)
		if err != nil || st == nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		st.Path = filepath.ToSlash(rel)
		key := filepath.ToSlash(filepath.Dir(rel))
		p := pkgs[key]
		if p == nil {
			p = &PkgStat{Package: key, Markers: map[string]int{}}
			pkgs[key] = p
		}
		p.Files++
		p.Total += st.Total
		p.Comment += st.Comment
		p.Doc += st.Doc
		p.Blank += st.Blank
		p.Code += st.Code
		for k, v := range st.Markers {
			p.Markers[k] += v
		}
		p.FileStats = append(p.FileStats, st)
		p.Hits = append(p.Hits, st.Hits...)
		return nil
	})
	return pkgs, err
}

// Sorted orders packages by descending ratio, ties by name so reports are deterministic.
func Sorted(pkgs map[string]*PkgStat) []*PkgStat {
	out := make([]*PkgStat, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ratio() != out[j].Ratio() {
			return out[i].Ratio() > out[j].Ratio()
		}
		return out[i].Package < out[j].Package
	})
	return out
}

// Total sums every package into one pseudo-package named "TOTAL".
func Total(pkgs map[string]*PkgStat) *PkgStat {
	t := &PkgStat{Package: "TOTAL", Markers: map[string]int{}}
	for _, p := range pkgs {
		t.Files += p.Files
		t.Total += p.Total
		t.Comment += p.Comment
		t.Doc += p.Doc
		t.Blank += p.Blank
		t.Code += p.Code
		for k, v := range p.Markers {
			t.Markers[k] += v
		}
	}
	return t
}
