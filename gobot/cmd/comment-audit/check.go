package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

// BaselineEntry is one package's recorded state.
type BaselineEntry struct {
	Ratio   float64 `json:"ratio"`
	Comment int     `json:"comment"`
	Total   int     `json:"total"`
	Markers int     `json:"markers"`
}

// Baseline is the recorded state a lane is checked for REGRESSION against.
// Absolute state is a separate question, answered by -max-ratio.
type Baseline struct {
	Packages map[string]BaselineEntry `json:"packages"`
}

// LoadBaseline reads a baseline file. A missing file is an error: read as empty it would
// let the check pass by accident, and a check that passes without running is worse than none.
func LoadBaseline(path string) (*Baseline, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bl Baseline
	if err := json.Unmarshal(b, &bl); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if bl.Packages == nil {
		bl.Packages = map[string]BaselineEntry{}
	}
	return &bl, nil
}

// NewBaseline snapshots the current scan.
func NewBaseline(pkgs map[string]*PkgStat) *Baseline {
	bl := &Baseline{Packages: map[string]BaselineEntry{}}
	for k, p := range pkgs {
		bl.Packages[k] = BaselineEntry{
			Ratio:   p.Ratio(),
			Comment: p.Comment,
			Total:   p.Total,
			Markers: p.MarkerTotal(),
		}
	}
	return bl
}

// WriteBaseline writes sorted, indented JSON so a re-record diffs cleanly.
func WriteBaseline(path string, bl *Baseline) error {
	b, err := json.MarshalIndent(bl, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// DefaultMaxFileProseRatio is the standing per-file ceiling on comment beyond
// the doc credit. A half is where the file stops being a file of code: past it
// the uncredited comment outweighs the code, the blanks and the credited godoc
// together — a definition, not a threshold fitted to what it catches. Being a
// ratio it is coarse on a short file and cannot tell one long godoc from an
// essay, so a file that reads badly under the bar is a §6 judgement, not a
// reason to move a number that currently means something.
const DefaultMaxFileProseRatio = 0.50

// GatePolicy is the standing check every lane is held to: no package denser
// than its recorded baseline, no file over the per-file ceiling. The Makefile
// self-check and the test that blocks the gate both call it, so neither carries
// a limit that could drift from the other's.
func GatePolicy(bl *Baseline, scope Scope) CheckOpts {
	return CheckOpts{
		Baseline:          bl,
		Scope:             scope,
		MaxMarkers:        -1,
		MaxFileProseRatio: DefaultMaxFileProseRatio,
	}
}

// Scope is the packages a run checks, and optionally the files the per-file ceiling answers for.
// There is no default matching: the gate must not descend, since a nested package's files are not
// its parent's and it would hold a lane to density it never opened, while -only must, since an
// operator naming a directory means everything under it.
type Scope struct {
	paths   []string
	subtree bool
	// files is what the ceiling applies to. NIL AND EMPTY DIFFER: nil is no filter, empty is none.
	files map[string]bool
}

// ExactPackages selects the packages named and nothing nested under them.
func ExactPackages(paths []string) Scope { return Scope{paths: paths} }

// PackageSubtrees selects the packages named and everything nested under them.
func PackageSubtrees(paths []string) Scope { return Scope{paths: paths, subtree: true} }

// ChangedFiles selects the packages named and holds the per-file ceiling to the files named.
func ChangedFiles(packages, files []string) Scope {
	set := make(map[string]bool, len(files))
	for _, f := range files {
		set[f] = true
	}
	return Scope{paths: packages, files: set}
}

// contains reports whether a package is checked. AN EMPTY SCOPE IS EVERY
// PACKAGE: it is the absence of a filter, not of packages.
func (s Scope) contains(pkg string) bool {
	if len(s.paths) == 0 {
		return true
	}
	for _, p := range s.paths {
		p = strings.TrimSuffix(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if pkg == p {
			return true
		}
		// On the separator, so internal/app does not sweep in internal/application.
		if s.subtree && strings.HasPrefix(pkg, p+"/") {
			return true
		}
	}
	return false
}

func (s Scope) containsFile(path string) bool {
	if s.files == nil {
		return true
	}
	return s.files[path]
}

// Violation is one package, or one file within it, that failed a check.
type Violation struct {
	Package string  `json:"package"`
	File    string  `json:"file,omitempty"`
	Kind    string  `json:"kind"`
	Ratio   float64 `json:"ratio"`
	Limit   float64 `json:"limit"`
	Detail  string  `json:"detail"`
}

func (v Violation) subject() string {
	if v.File != "" {
		return v.File
	}
	return v.Package
}

// String renders a violation. Ratios carry two decimals because a regression is
// often a fraction of a point and "53.3% > 53.3%" reads as a tool bug.
func (v Violation) String() string {
	if v.Ratio == 0 && v.Limit == 0 {
		return fmt.Sprintf("%s: %s — %s", v.subject(), v.Kind, v.Detail)
	}
	return fmt.Sprintf("%s: %s (%.2f%% > %.2f%%) %s",
		v.subject(), v.Kind, v.Ratio*100, v.Limit*100, v.Detail)
}

// CheckOpts configures a gate run. Every limit is a flag whose help carries its
// reasoning; a field documents only what the command line cannot say.
type CheckOpts struct {
	// Baseline, when non-nil, fails any package whose ratio rose above its
	// recorded value by more than Tolerance.
	Baseline *Baseline
	// MaxRatio also holds a package the baseline does not know, which is what
	// stops a lane adding a fresh dense one.
	MaxRatio  float64
	Tolerance float64
	// Scope, when non-empty, restricts the check to the packages it selects.
	Scope      Scope
	MaxMarkers int
	// MaxFileProseRatio is a bar, not a ratchet: a file cannot inherit the
	// right to be an essay.
	MaxFileProseRatio float64
}

// Check applies the gate rules, ordered by package. Empty means the run passes.
func Check(pkgs map[string]*PkgStat, opts CheckOpts) []Violation {
	var out []Violation
	for _, p := range Sorted(pkgs) {
		if !opts.Scope.contains(p.Package) {
			continue
		}
		ratio := p.Ratio()

		if opts.Baseline != nil {
			base, known := opts.Baseline.Packages[p.Package]
			switch {
			case known && ratio > base.Ratio+opts.Tolerance:
				out = append(out, Violation{
					Package: p.Package, Kind: "regression",
					Ratio: ratio, Limit: base.Ratio + opts.Tolerance,
					Detail: fmt.Sprintf("baseline %.1f%% (%d/%d) -> now %d/%d",
						base.Ratio*100, base.Comment, base.Total, p.Comment, p.Total),
				})
			case !known && opts.MaxRatio > 0 && ratio > opts.MaxRatio:
				out = append(out, Violation{
					Package: p.Package, Kind: "new package over max-ratio",
					Ratio: ratio, Limit: opts.MaxRatio,
					Detail: fmt.Sprintf("%d/%d lines", p.Comment, p.Total),
				})
			}
			continue
		}

		if opts.MaxRatio > 0 && ratio > opts.MaxRatio {
			out = append(out, Violation{
				Package: p.Package, Kind: "over max-ratio",
				Ratio: ratio, Limit: opts.MaxRatio,
				Detail: fmt.Sprintf("%d/%d lines", p.Comment, p.Total),
			})
		}
	}

	if opts.MaxFileProseRatio > 0 {
		for _, p := range Sorted(pkgs) {
			if !opts.Scope.contains(p.Package) {
				continue
			}
			for _, f := range p.FileStats {
				if !opts.Scope.containsFile(f.Path) {
					continue
				}
				if f.ProseRatio() <= opts.MaxFileProseRatio {
					continue
				}
				out = append(out, Violation{
					Package: p.Package, File: f.Path, Kind: "file over max-file-prose",
					Ratio: f.ProseRatio(), Limit: opts.MaxFileProseRatio,
					Detail: fmt.Sprintf("%d of %d lines are comment the doc credit does not cover; cut ~%d to clear — %s",
						f.ProseLines(), f.Total, LinesToCut(f.ProseLines(), f.Total, opts.MaxFileProseRatio),
						blockAdvice(f.Blocks)),
				})
			}
		}
	}

	if opts.MaxMarkers >= 0 {
		for _, p := range Sorted(pkgs) {
			if !opts.Scope.contains(p.Package) {
				continue
			}
			if n := p.MarkerTotal(); n > opts.MaxMarkers {
				out = append(out, Violation{
					Package: p.Package, Kind: "archaeology markers",
					Detail: fmt.Sprintf("%d markers > max %d (%s)",
						n, opts.MaxMarkers, markerBreakdown(p.Markers)),
				})
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		return out[i].File < out[j].File
	})
	return out
}

// LinesToCut is how many comment lines must leave a file for it to sit at or
// under limit. Deleting a comment line shrinks the file as well as its comment
// count, so both sides move: the answer solves (prose-k)/(total-k) <= limit.
func LinesToCut(prose, total int, limit float64) int {
	if total == 0 || limit >= 1 {
		return 0
	}
	k := (float64(prose) - limit*float64(total)) / (1 - limit)
	if k <= 0 {
		return 0
	}
	return int(math.Ceil(k))
}

// blockAdvice names the longest runs of uncredited comment, sending the reader
// to the bulk rather than to the file.
func blockAdvice(blocks []ProseBlock) string {
	if len(blocks) == 0 {
		return "no contiguous block found"
	}
	if len(blocks) > 3 {
		blocks = blocks[:3]
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, fmt.Sprintf(":%d (%d lines)", b.Line, b.Lines))
	}
	return "longest blocks at " + strings.Join(parts, ", ")
}

// markerBreakdown renders a package's marker counts deterministically.
func markerBreakdown(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}
