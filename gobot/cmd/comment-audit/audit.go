package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Marker is an archaeology pattern: prose whose truth depends on the history of
// the file rather than on the code as it stands. See ENGINEERING.md §6.
type Marker struct {
	Name string
	Re   *regexp.Regexp
}

// Markers are matched against comment text only, never against code. Each is
// advisory by default; -max-markers turns them into a failure condition.
var Markers = []Marker{
	{"bead-id", regexp.MustCompile(`\bsp-[a-z0-9]{4}`)},
	{"measured live", regexp.MustCompile(`(?i)measured live`)},
	{"the defect", regexp.MustCompile(`(?i)the defect`)},
	{"used to", regexp.MustCompile(`(?i)\bused to\b`)},
	{"this fixed", regexp.MustCompile(`(?i)this fixed`)},
	{"previously", regexp.MustCompile(`(?i)\bpreviously\b`)},
	{"was found", regexp.MustCompile(`(?i)\bwas found\b`)},
}

// generatedRe matches the standard Go generated-file banner. Generated files are
// excluded: their comment density is not a property anybody can edit.
var generatedRe = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// MarkerHit locates one archaeology marker for the -explain report.
type MarkerHit struct {
	Marker string `json:"marker"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
}

// FileStat is the line census of one source file. Comment+Blank+Code == Total.
type FileStat struct {
	Path    string         `json:"path"`
	Total   int            `json:"total"`
	Comment int            `json:"comment"`
	Blank   int            `json:"blank"`
	Code    int            `json:"code"`
	Markers map[string]int `json:"markers,omitempty"`
	Hits    []MarkerHit    `json:"-"`
}

// Ratio is the comment share of ALL lines, blanks included. That denominator is
// deliberate: it is stable under reflowing and it is the number the bead quotes.
func (f FileStat) Ratio() float64 {
	if f.Total == 0 {
		return 0
	}
	return float64(f.Comment) / float64(f.Total)
}

// PkgStat aggregates a directory's non-test, non-generated files.
type PkgStat struct {
	Package string         `json:"package"`
	Files   int            `json:"files"`
	Total   int            `json:"total"`
	Comment int            `json:"comment"`
	Blank   int            `json:"blank"`
	Code    int            `json:"code"`
	Markers map[string]int `json:"markers,omitempty"`
	Hits    []MarkerHit    `json:"-"`
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

// AnalyzeFile censuses one file's lines. src must parse as Go: a comment census
// built by regex is fooled by "//" inside a string literal, so the comment
// extents come from the parser.
//
// Reports (nil, nil) for a file that is excluded by policy (generated banner).
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

	for i, l := range lines {
		lineNo := i + 1
		switch {
		case isComment[lineNo]:
			st.Comment++
		case strings.TrimSpace(l) == "":
			st.Blank++
		default:
			st.Code++
		}
	}
	if len(st.Markers) == 0 {
		st.Markers = nil
	}
	return st, nil
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

// skipDirs are never walked. testdata and vendor are not ours to police;
// node_modules and .git are noise; .claude holds sibling worktrees, which would
// otherwise be censused as if they were this tree.
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
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		p := pkgs[key]
		if p == nil {
			p = &PkgStat{Package: key, Markers: map[string]int{}}
			pkgs[key] = p
		}
		p.Files++
		p.Total += st.Total
		p.Comment += st.Comment
		p.Blank += st.Blank
		p.Code += st.Code
		for k, v := range st.Markers {
			p.Markers[k] += v
		}
		p.Hits = append(p.Hits, st.Hits...)
		return nil
	})
	return pkgs, err
}

// Sorted returns packages ordered by descending comment ratio, ties broken by
// name so the report is deterministic.
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
		t.Blank += p.Blank
		t.Code += p.Code
		for k, v := range p.Markers {
			t.Markers[k] += v
		}
	}
	return t
}
