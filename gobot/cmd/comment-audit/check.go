package main

import (
	"encoding/json"
	"fmt"
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

// LoadBaseline reads a baseline file. A missing file is an error: silently
// treating it as empty would make the check pass by accident, and a check that
// passes when it did not run is worse than no check.
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

// WriteBaseline persists a baseline as indented JSON with sorted keys, so a
// regenerated baseline diffs cleanly.
func WriteBaseline(path string, bl *Baseline) error {
	b, err := json.MarshalIndent(bl, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Violation is one package that failed a check.
type Violation struct {
	Package string  `json:"package"`
	Kind    string  `json:"kind"`
	Ratio   float64 `json:"ratio"`
	Limit   float64 `json:"limit"`
	Detail  string  `json:"detail"`
}

// String renders a violation. Ratios carry two decimals because a regression is
// often a fraction of a point, and "53.3% > 53.3%" reads as a tool bug.
// A marker violation has no ratio, so it prints none.
func (v Violation) String() string {
	if v.Ratio == 0 && v.Limit == 0 {
		return fmt.Sprintf("%s: %s — %s", v.Package, v.Kind, v.Detail)
	}
	return fmt.Sprintf("%s: %s (%.2f%% > %.2f%%) %s",
		v.Package, v.Kind, v.Ratio*100, v.Limit*100, v.Detail)
}

// CheckOpts configures a gate run.
type CheckOpts struct {
	// Baseline, when non-nil, fails any package whose ratio rose above its
	// recorded value by more than Tolerance.
	Baseline *Baseline
	// MaxRatio is an absolute ceiling, applied to every checked package. Zero
	// disables it. A package absent from the baseline is held to this bar,
	// which is what stops a lane from adding a fresh 60% package.
	MaxRatio float64
	// Tolerance is the ratio increase forgiven before a baseline regression
	// fires, as an absolute ratio delta (0.005 == half a point). Zero is the
	// intended setting: any proportional slack is really a per-line budget that
	// scales with package size, so the same 0.005 buys ~45 free comment lines
	// in a 9k-line package and less than one in a 100-line package. A lane that
	// trips it re-earns the room by cutting archaeology.
	Tolerance float64
	// Only, when non-empty, restricts the check to packages under these
	// slash-separated path prefixes — a lane's touched set.
	Only []string
	// MaxMarkers fails any checked package carrying more archaeology markers
	// than this. Negative disables it.
	MaxMarkers int
}

// inScope reports whether pkg is under one of the Only prefixes.
func (o CheckOpts) inScope(pkg string) bool {
	if len(o.Only) == 0 {
		return true
	}
	for _, p := range o.Only {
		p = strings.TrimSuffix(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if pkg == p || strings.HasPrefix(pkg, p+"/") {
			return true
		}
	}
	return false
}

// Check applies the gate rules and returns every violation, ordered by package.
// An empty result means the run passes.
func Check(pkgs map[string]*PkgStat, opts CheckOpts) []Violation {
	var out []Violation
	for _, p := range Sorted(pkgs) {
		if !opts.inScope(p.Package) {
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

	if opts.MaxMarkers >= 0 {
		for _, p := range Sorted(pkgs) {
			if !opts.inScope(p.Package) {
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

	sort.SliceStable(out, func(i, j int) bool { return out[i].Package < out[j].Package })
	return out
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
