package main

import (
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
)

// A lane is answerable for the packages it changed. Density it inherited is
// somebody else's regression and blocking on it does not make anyone fix it —
// it blocks the sweep that would.

func denseTree() (map[string]*PkgStat, *Baseline) {
	pkgs := map[string]*PkgStat{
		"internal/mine": {Package: "internal/mine", Comment: 60, Total: 100, FileStats: []*FileStat{
			{Path: "internal/mine/essay.go", Total: 100, Comment: 60, Doc: 5},
		}},
		"internal/theirs": {Package: "internal/theirs", Comment: 70, Total: 100, FileStats: []*FileStat{
			{Path: "internal/theirs/essay.go", Total: 100, Comment: 70, Doc: 5},
		}},
	}
	bl := &Baseline{Packages: map[string]BaselineEntry{
		"internal/mine":   {Ratio: 0.10, Comment: 10, Total: 100},
		"internal/theirs": {Ratio: 0.10, Comment: 10, Total: 100},
	}}
	return pkgs, bl
}

// TestScopedGateHoldsALaneToItsOwnPackages: both packages here are over the
// ceiling and both are denser than their baseline, so a tree-wide run reports
// four violations no matter who ran it.
func TestScopedGateHoldsALaneToItsOwnPackages(t *testing.T) {
	pkgs, bl := denseTree()

	got := scopedGateViolations(pkgs, bl, []string{"internal/mine"}, []string{"internal/mine/essay.go"})
	if len(got) == 0 {
		t.Fatal("a package the lane changed must still fail: scoping the gate is not softening it")
	}
	for _, v := range got {
		if v.Package != "internal/mine" {
			t.Fatalf("violation reported against %q, which this lane did not touch: %s", v.Package, v)
		}
	}
	if len(got) != 2 {
		t.Fatalf("violations = %v, want both the regression and the file ceiling for internal/mine", got)
	}
}

// The other half of the same rule, and the half that makes the pipeline work: a
// package this lane never opened cannot block it, so the sweep that cleans that
// package can be merged instead of being held behind the mess it removes.
func TestScopedGateIgnoresDensityTheLaneDidNotCause(t *testing.T) {
	pkgs, bl := denseTree()

	if got := scopedGateViolations(pkgs, bl, []string{"internal/mine"}, []string{"internal/mine/essay.go"}); len(got) != 2 {
		t.Fatalf("fixture check: internal/mine should have 2 violations, got %v", got)
	}
	got := scopedGateViolations(pkgs, bl, []string{"internal/elsewhere"}, nil)
	if len(got) != 0 {
		t.Fatalf("violations = %v, want none: internal/theirs is dense, but this lane changed neither file in it", got)
	}
}

// TestScopedGateWithNoChangesChecksNothing pins the trap in the filter's own
// semantics. A scope treats empty as "no filter", so handing it an empty touched
// set does not check nothing — it checks EVERYTHING, and turns the ratchet back
// into the tree-wide bar on precisely the runs that changed no Go source. The
// early return is load-bearing, not a shortcut.
func TestScopedGateWithNoChangesChecksNothing(t *testing.T) {
	pkgs, bl := denseTree()

	for _, touched := range [][]string{nil, {}} {
		if got := scopedGateViolations(pkgs, bl, touched, nil); len(got) != 0 {
			t.Fatalf("touched=%v produced %d violation(s) in a tree the lane did not change: "+
				"an empty scope reached Check, where empty means unfiltered", touched, len(got))
		}
	}
	// And the same tree, unfiltered, is what that mistake would have reported —
	// this is the call the early return stops being made.
	if got := Check(pkgs, GatePolicy(bl, ExactPackages(nil))); len(got) != 4 {
		t.Fatalf("unfiltered = %v, want 4: the fixture no longer demonstrates what the early return prevents", got)
	}
}

// A parent package and one nested under it, both over both limits, where the
// lane changed only the parent's files.
func nestedTree() (map[string]*PkgStat, *Baseline) {
	pkgs := map[string]*PkgStat{
		"internal/parent": {Package: "internal/parent", Comment: 60, Total: 100, FileStats: []*FileStat{
			{Path: "internal/parent/essay.go", Total: 100, Comment: 60, Doc: 5},
		}},
		"internal/parent/child": {Package: "internal/parent/child", Comment: 70, Total: 100, FileStats: []*FileStat{
			{Path: "internal/parent/child/essay.go", Total: 100, Comment: 70, Doc: 5},
		}},
	}
	bl := &Baseline{Packages: map[string]BaselineEntry{
		"internal/parent":       {Ratio: 0.10, Comment: 10, Total: 100},
		"internal/parent/child": {Ratio: 0.10, Comment: 10, Total: 100},
	}}
	return pkgs, bl
}

// The gate's scope is exact, and this is the quiet form of the density-you-did-
// not-cause defect: a lane that opened one file in a parent package answering
// for every package underneath it. The child is a separate package whose files
// this lane never touched, so §6a covers it exactly as it covers a package on
// the other side of the tree.
//
// Its pair below is the calibration this test needs: the child is a live
// offender there, so the silence here is the scope declining to report it and
// not a fixture that had nothing to say.
func TestGateScopeDoesNotDescendIntoAPackageTheLaneDidNotOpen(t *testing.T) {
	pkgs, bl := nestedTree()

	got := scopedGateViolations(pkgs, bl, []string{"internal/parent"}, []string{"internal/parent/essay.go"})
	if len(got) != 2 {
		t.Fatalf("violations = %v, want the parent's regression and its file ceiling: "+
			"the package this lane did change must still fail", got)
	}
	for _, v := range got {
		if v.Package != "internal/parent" {
			t.Fatalf("the gate flagged %q, a package nested under the one this lane changed "+
				"but whose own files it never touched: %s", v.Package, v)
		}
	}
}

// The other half, and the reason the gate cannot simply reuse -only's matching:
// -only IS a subtree selector. An operator naming a directory means everything
// under it, so narrowing this to exact membership would silently shrink every
// invocation already written.
func TestOnlySelectsTheWholeSubtree(t *testing.T) {
	pkgs, bl := nestedTree()

	got := Check(pkgs, GatePolicy(bl, PackageSubtrees([]string{"internal/parent"})))
	if len(got) != 4 {
		t.Fatalf("violations = %v, want all four — both packages over both limits: "+
			"-only names a subtree, and the nested package is inside the one named", got)
	}
}

func TestPackageDirsSelectsOnlyTheFilesTheCensusReads(t *testing.T) {
	got := packageDirs([]string{
		"internal/domain/hullbuy/price.go",
		"internal/domain/hullbuy/price_test.go", // tests are not censused
		"internal/domain/hullbuy/fixtures.json", // nor is anything else
		"Makefile",
		"cmd/comment-audit/main.go",
		"cmd/comment-audit/check.go", // second file, same package
		"main.go",                    // the module root package
	})
	want := []string{".", "cmd/comment-audit", "internal/domain/hullbuy"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packageDirs = %v, want %v", got, want)
	}
}

// A rename must name the directory the file LEFT as well as the one it arrived
// in: the package it left lost those lines and its ratio moved too. `git diff`
// is asked for --no-renames for exactly this reason, so both paths appear.
func TestPackageDirsCoversBothSidesOfAMove(t *testing.T) {
	got := packageDirs([]string{"internal/old/thing.go", "internal/new/thing.go"})
	want := []string{"internal/new", "internal/old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("packageDirs = %v, want %v: the package a file left is as changed as the one it joined", got, want)
	}
}

// A tree with no Go changes has no scope, which is distinct from an unfiltered
// one — see TestScopedGateWithNoChangesChecksNothing for what that distinction
// is worth.
func TestPackageDirsOfNoGoChangesIsEmpty(t *testing.T) {
	if got := packageDirs([]string{"README.md", "gobot/Makefile", "a/b_test.go"}); len(got) != 0 {
		t.Fatalf("packageDirs = %v, want none: none of those files is in the census", got)
	}
}

// The scan never enters these directories, so a package inside one is absent
// from the census for a declared reason. The calibration that hunts for missing
// packages has to know that, or a lane adding Go source under testdata is failed
// for a root mismatch that is not there.
func TestCensusExcludedDirsAreNotReadAsAMissingPackage(t *testing.T) {
	for _, dir := range []string{"internal/foo/testdata", "vendor/x/y", "internal/x/node_modules/y"} {
		if !isCensusExcluded(dir) {
			t.Errorf("%q is skipped by the scan but not by the calibration: the gate would fail "+
				"claiming git and the census disagree about paths", dir)
		}
	}
	if isCensusExcluded("internal/domain/fleetgrowth") {
		t.Error("an ordinary package was treated as excluded: the calibration would stop noticing a real mismatch")
	}
}

func TestLooksLikeSHARejectsWhatIsNotACommit(t *testing.T) {
	if !looksLikeSHA("4a865bb3f0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5") {
		t.Fatal("a full commit id must be accepted")
	}
	for _, bad := range []string{"", "main", "HEAD", "abc", "fatal: no such ref", "4a865bZZ"} {
		if looksLikeSHA(bad) {
			t.Fatalf("%q was taken for a commit id: it would be handed to git diff as a revision", bad)
		}
	}
}

// -gate REPLACES every limit with the standing policy's own. A limit the caller
// also wrote can therefore only be discarded, and discarding it silently is the
// same defect as the split -only list: the run checks something other than what
// was asked and still reports OK.

func parseArgs(t *testing.T, args ...string) (*cliFlags, map[string]bool) {
	t.Helper()
	fs := flag.NewFlagSet("comment-audit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c := registerFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parsing %v: %v", args, err)
	}
	return c, flagsSet(fs)
}

func TestGateRefusesTheLimitsItWouldOverride(t *testing.T) {
	bl := &Baseline{Packages: map[string]BaselineEntry{"p": {}}}
	for _, tc := range []struct{ arg, name string }{
		{"-max-ratio=0.4", "-max-ratio"},
		{"-tolerance=0.05", "-tolerance"},
		{"-max-markers=3", "-max-markers"},
		{"-max-file-prose=0.9", "-max-file-prose"},
	} {
		_, set := parseArgs(t, "-gate", tc.arg)
		_, err := gatePolicyFor(bl, Scope{}, set)
		if err == nil {
			t.Fatalf("-gate %s was accepted: the gate policy would overwrite it and the run would "+
				"silently check a limit the caller did not ask for", tc.arg)
		}
		if !strings.Contains(err.Error(), tc.name) {
			t.Fatalf("the refusal must name the flag it refused, got: %v", err)
		}
		if !strings.Contains(err.Error(), "-gate") {
			t.Fatalf("the refusal must name the other half of the conflict, got: %v", err)
		}
	}
}

// The refusal is only correct if it is narrow: -gate exists to be used, and
// flags the policy does not own must still work with it.
func TestGateAcceptsTheFlagsItDoesNotOwn(t *testing.T) {
	bl := &Baseline{Packages: map[string]BaselineEntry{"p": {}}}
	_, set := parseArgs(t, "-gate", "-root=.", "-quiet", "-top=5", "-explain", "-only=internal/x")
	want := PackageSubtrees([]string{"internal/x"})
	opts, err := gatePolicyFor(bl, want, set)
	if err != nil {
		t.Fatalf("gatePolicyFor: %v", err)
	}
	if opts.MaxFileProseRatio != DefaultMaxFileProseRatio || opts.Baseline != bl {
		t.Fatalf("opts = %+v, want the standing policy carrying the baseline", opts)
	}
	// Both the packages and which matching they asked for: -gate replaces limits,
	// never the caller's scope.
	if !reflect.DeepEqual(opts.Scope, want) {
		t.Fatalf("Scope = %+v, want %+v preserved through the policy", opts.Scope, want)
	}
}

// A defaulted limit is not a passed one. flag.Visit is what tells the two apart,
// and a version that inspected values instead would refuse every plain -gate run
// whose default happened to be non-zero.
func TestGateAcceptsAPlainInvocation(t *testing.T) {
	bl := &Baseline{Packages: map[string]BaselineEntry{"p": {}}}
	_, set := parseArgs(t, "-gate")
	if _, err := gatePolicyFor(bl, Scope{}, set); err != nil {
		t.Fatalf("a plain -gate run must work: %v", err)
	}
}

// Without a baseline the ratchet has nothing to compare against, so only the
// per-file ceiling would run — and a package the lane made denser would report
// OK. That is a pass that did not happen.
func TestGateRefusesWithoutABaseline(t *testing.T) {
	_, set := parseArgs(t, "-gate")
	_, err := gatePolicyFor(nil, Scope{}, set)
	if err == nil {
		t.Fatal("-gate without -baseline was accepted: the ratchet cannot run and the OK would be meaningless")
	}
	if !strings.Contains(err.Error(), "-baseline") {
		t.Fatalf("the refusal must name what is missing, got: %v", err)
	}
}

// A name in gateOwnedFlags that is not a real flag never matches anything
// flag.Visit reports, so the refusal quietly stops covering that limit and we
// are back to the silent override.
func TestGateOwnedFlagsAreRealFlags(t *testing.T) {
	fs := flag.NewFlagSet("comment-audit", flag.ContinueOnError)
	registerFlags(fs)
	for _, name := range gateOwnedFlags {
		if fs.Lookup(name) == nil {
			t.Errorf("gateOwnedFlags names %q, which is not a flag: nothing would ever conflict with it", name)
		}
	}
}

// The list is also a completeness claim, and this is the ratchet on it: every
// limit GatePolicy sets must be a flag the caller can be refused for. Baseline
// and Scope are excluded because the policy passes those through rather than
// replacing them.
func TestGateOwnedFlagsCoverEveryLimitThePolicyReplaces(t *testing.T) {
	passedThrough := map[string]bool{"Baseline": true, "Scope": true}
	var replaced []string
	ty := reflect.TypeOf(CheckOpts{})
	for i := 0; i < ty.NumField(); i++ {
		if name := ty.Field(i).Name; !passedThrough[name] {
			replaced = append(replaced, name)
		}
	}
	if len(replaced) != len(gateOwnedFlags) {
		t.Fatalf("CheckOpts has %d limit(s) %v but gateOwnedFlags names %d %v: a limit with no "+
			"flag in that list is one -gate can still overwrite without saying so",
			len(replaced), replaced, len(gateOwnedFlags), gateOwnedFlags)
	}
}
