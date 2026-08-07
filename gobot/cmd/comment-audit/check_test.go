package main

import (
	"os"
	"path/filepath"
	"testing"
)

func pkg(name string, comment, total int, markers int) *PkgStat {
	p := &PkgStat{Package: name, Comment: comment, Total: total, Files: 1}
	if markers > 0 {
		p.Markers = map[string]int{"bead-id": markers}
	}
	return p
}

func TestCheckPassesWhenRatioHoldsAtBaseline(t *testing.T) {
	pkgs := map[string]*PkgStat{"a": pkg("a", 5, 10, 0)}
	bl := &Baseline{Packages: map[string]BaselineEntry{
		"a": {Ratio: 0.5, Comment: 5, Total: 10},
	}}
	if v := Check(pkgs, CheckOpts{Baseline: bl, MaxMarkers: -1}); len(v) != 0 {
		t.Fatalf("violations = %v, want none", v)
	}
}

func TestCheckFailsOnRegressionAboveBaseline(t *testing.T) {
	pkgs := map[string]*PkgStat{"a": pkg("a", 8, 10, 0)}
	bl := &Baseline{Packages: map[string]BaselineEntry{
		"a": {Ratio: 0.5, Comment: 5, Total: 10},
	}}
	v := Check(pkgs, CheckOpts{Baseline: bl, MaxMarkers: -1})
	if len(v) != 1 || v[0].Kind != "regression" {
		t.Fatalf("violations = %+v, want one regression", v)
	}
}

// A package already over the absolute bar must not block a lane that leaves it
// alone; that is the whole reason the gate check is a regression check.
func TestCheckDoesNotFailAnInheritedlyDensePackage(t *testing.T) {
	pkgs := map[string]*PkgStat{"a": pkg("a", 6, 10, 0)}
	bl := &Baseline{Packages: map[string]BaselineEntry{
		"a": {Ratio: 0.6, Comment: 6, Total: 10},
	}}
	v := Check(pkgs, CheckOpts{Baseline: bl, MaxRatio: 0.30, MaxMarkers: -1})
	if len(v) != 0 {
		t.Fatalf("violations = %+v, want none", v)
	}
}

func TestCheckImprovementIsNeverAViolation(t *testing.T) {
	pkgs := map[string]*PkgStat{"a": pkg("a", 2, 10, 0)}
	bl := &Baseline{Packages: map[string]BaselineEntry{
		"a": {Ratio: 0.5, Comment: 5, Total: 10},
	}}
	if v := Check(pkgs, CheckOpts{Baseline: bl, MaxMarkers: -1}); len(v) != 0 {
		t.Fatalf("violations = %+v, want none", v)
	}
}

func TestCheckToleranceForgivesASmallRise(t *testing.T) {
	pkgs := map[string]*PkgStat{"a": pkg("a", 501, 1000, 0)}
	bl := &Baseline{Packages: map[string]BaselineEntry{
		"a": {Ratio: 0.5, Comment: 500, Total: 1000},
	}}
	if v := Check(pkgs, CheckOpts{Baseline: bl, Tolerance: 0.005, MaxMarkers: -1}); len(v) != 0 {
		t.Fatalf("violations = %+v, want none inside tolerance", v)
	}
	if v := Check(pkgs, CheckOpts{Baseline: bl, Tolerance: 0, MaxMarkers: -1}); len(v) != 1 {
		t.Fatalf("violations = %+v, want one with zero tolerance", v)
	}
}

// A lane cannot dodge the bar by putting the prose in a brand-new package.
func TestCheckHoldsANewPackageToMaxRatio(t *testing.T) {
	pkgs := map[string]*PkgStat{"fresh": pkg("fresh", 6, 10, 0)}
	bl := &Baseline{Packages: map[string]BaselineEntry{}}
	v := Check(pkgs, CheckOpts{Baseline: bl, MaxRatio: 0.30, MaxMarkers: -1})
	if len(v) != 1 || v[0].Kind != "new package over max-ratio" {
		t.Fatalf("violations = %+v, want one new-package violation", v)
	}
	if v := Check(pkgs, CheckOpts{Baseline: bl, MaxMarkers: -1}); len(v) != 0 {
		t.Fatalf("violations = %+v, want none when max-ratio is disabled", v)
	}
}

func TestCheckMaxRatioWithoutBaselineIsAbsolute(t *testing.T) {
	pkgs := map[string]*PkgStat{
		"dense": pkg("dense", 6, 10, 0),
		"lean":  pkg("lean", 1, 10, 0),
	}
	v := Check(pkgs, CheckOpts{MaxRatio: 0.30, MaxMarkers: -1})
	if len(v) != 1 || v[0].Package != "dense" || v[0].Kind != "over max-ratio" {
		t.Fatalf("violations = %+v, want only dense", v)
	}
}

func TestCheckOnlyRestrictsToTouchedPackages(t *testing.T) {
	pkgs := map[string]*PkgStat{
		"internal/application/fleet":  pkg("internal/application/fleet", 8, 10, 0),
		"internal/application/bootup": pkg("internal/application/bootup", 8, 10, 0),
		"internal/adapters/db":        pkg("internal/adapters/db", 8, 10, 0),
	}
	bl := &Baseline{Packages: map[string]BaselineEntry{
		"internal/application/fleet":  {Ratio: 0.5, Comment: 5, Total: 10},
		"internal/application/bootup": {Ratio: 0.5, Comment: 5, Total: 10},
		"internal/adapters/db":        {Ratio: 0.5, Comment: 5, Total: 10},
	}}
	v := Check(pkgs, CheckOpts{
		Baseline: bl, MaxMarkers: -1,
		Scope: PackageSubtrees([]string{"internal/application/fleet"}),
	})
	if len(v) != 1 || v[0].Package != "internal/application/fleet" {
		t.Fatalf("violations = %+v, want only the touched package", v)
	}
}

// A prefix names a subtree, not a string prefix: "internal/app" must not sweep
// in "internal/application".
func TestCheckOnlyMatchesPathSegmentsNotSubstrings(t *testing.T) {
	pkgs := map[string]*PkgStat{
		"internal/application": pkg("internal/application", 8, 10, 0),
		"internal/app":         pkg("internal/app", 8, 10, 0),
		"internal/app/inner":   pkg("internal/app/inner", 8, 10, 0),
	}
	bl := &Baseline{Packages: map[string]BaselineEntry{
		"internal/application": {Ratio: 0.5},
		"internal/app":         {Ratio: 0.5},
		"internal/app/inner":   {Ratio: 0.5},
	}}
	v := Check(pkgs, CheckOpts{Baseline: bl, MaxMarkers: -1, Scope: PackageSubtrees([]string{"internal/app"})})
	if len(v) != 2 {
		t.Fatalf("violations = %+v, want internal/app and internal/app/inner only", v)
	}
	for _, got := range v {
		if got.Package == "internal/application" {
			t.Fatal("internal/application matched as a substring of internal/app")
		}
	}
}

func TestCheckOnlyToleratesTrailingSlashAndSpaces(t *testing.T) {
	pkgs := map[string]*PkgStat{"a/b": pkg("a/b", 8, 10, 0)}
	bl := &Baseline{Packages: map[string]BaselineEntry{"a/b": {Ratio: 0.5}}}
	if v := Check(pkgs, CheckOpts{Baseline: bl, MaxMarkers: -1, Scope: PackageSubtrees([]string{" a/b/ "})}); len(v) != 1 {
		t.Fatalf("violations = %+v, want one", v)
	}
}

func TestCheckMaxMarkersFiresIndependentlyOfRatio(t *testing.T) {
	pkgs := map[string]*PkgStat{"a": pkg("a", 1, 100, 7)}
	v := Check(pkgs, CheckOpts{MaxMarkers: 0})
	if len(v) != 1 || v[0].Kind != "archaeology markers" {
		t.Fatalf("violations = %+v, want one marker violation", v)
	}
	if v := Check(pkgs, CheckOpts{MaxMarkers: -1}); len(v) != 0 {
		t.Fatalf("violations = %+v, want none when disabled", v)
	}
}

func TestBaselineRoundTrip(t *testing.T) {
	pkgs := map[string]*PkgStat{
		"a": pkg("a", 5, 10, 3),
		"b": pkg("b", 1, 10, 0),
	}
	path := filepath.Join(t.TempDir(), "base.json")
	if err := WriteBaseline(path, NewBaseline(pkgs)); err != nil {
		t.Fatalf("WriteBaseline: %v", err)
	}
	bl, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if got := bl.Packages["a"]; got.Ratio != 0.5 || got.Comment != 5 || got.Markers != 3 {
		t.Fatalf("round-tripped a = %+v", got)
	}
	if v := Check(pkgs, CheckOpts{Baseline: bl, MaxMarkers: -1}); len(v) != 0 {
		t.Fatalf("a baseline must validate against the tree it came from: %+v", v)
	}
}

// A missing baseline must be an error, never an empty baseline: a check that
// passes because it did not run is worse than no check.
func TestLoadBaselineMissingFileIsAnError(t *testing.T) {
	if _, err := LoadBaseline(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("want an error for a missing baseline, got nil")
	}
}

func TestLoadBaselineRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(path); err == nil {
		t.Fatal("want an error for malformed JSON, got nil")
	}
}

func TestViolationStringCarriesBothNumbers(t *testing.T) {
	v := Violation{Package: "a", Kind: "regression", Ratio: 0.53, Limit: 0.30, Detail: "d"}
	if got := v.String(); got != "a: regression (53.00% > 30.00%) d" {
		t.Fatalf("String() = %q", got)
	}
}

// A fractional regression must not render as "53.3% > 53.3%", which reads as a
// tool bug rather than as a finding.
func TestViolationStringSeparatesNearIdenticalRatios(t *testing.T) {
	v := Violation{Package: "a", Kind: "regression", Ratio: 0.53318, Limit: 0.53286, Detail: "d"}
	if got := v.String(); got != "a: regression (53.32% > 53.29%) d" {
		t.Fatalf("String() = %q", got)
	}
}

func TestViolationStringOmitsRatioWhenThereIsNone(t *testing.T) {
	v := Violation{Package: "a", Kind: "archaeology markers", Detail: "53 markers > max 0"}
	if got := v.String(); got != "a: archaeology markers — 53 markers > max 0" {
		t.Fatalf("String() = %q", got)
	}
}

func TestMarkerBreakdownIsDeterministic(t *testing.T) {
	m := map[string]int{"used to": 1, "bead-id": 2, "the defect": 3}
	if got := markerBreakdown(m); got != "bead-id=2 the defect=3 used to=1" {
		t.Fatalf("markerBreakdown = %q", got)
	}
}
