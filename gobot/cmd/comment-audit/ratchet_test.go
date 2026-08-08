package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ratchet's subject is what a lane WROTE, not what the ratio did. Removing code leaner
// than the package raises the remainder arithmetically — no prose need be involved — and a
// ratio comparison cannot tell that apart from prose somebody added. The cases below are the
// ones that separate the two readings, in both directions.

// anchor is a package as the comparison found it, with the ratio derived rather than stated so
// a fixture cannot claim a density its own counts contradict.
func anchor(comment, total int) BaselineEntry {
	return BaselineEntry{Ratio: float64(comment) / float64(total), Comment: comment, Total: total}
}

// ratchet applies the package half of the standing policy to one package against one anchor.
func ratchet(t *testing.T, base BaselineEntry, comment, total int) []Violation {
	t.Helper()
	pkgs := map[string]*PkgStat{"p": pkg("p", comment, total, 0)}
	bl := &Baseline{Packages: map[string]BaselineEntry{"p": base}}
	return Check(pkgs, CheckOpts{Baseline: bl, MaxMarkers: -1})
}

// THE CASE THE RULE EXISTS TO GET RIGHT: a lane deletes 4,277 lines whose comment share sat
// BELOW the package's own, the ratio climbs nearly three points, and not one comment line was
// written. Under a ratio comparison the only way out is to cut live prose from the survivors —
// roughly a tenth of everything the package still says — which buys nothing a reader wanted.
func TestRatchetPassesADeletionThatRaisesTheRatio(t *testing.T) {
	base := anchor(3635, 9846)

	if got := ratchet(t, base, 2212, 5569); len(got) != 0 {
		t.Fatalf("violations = %v, want none: this lane removed 4277 lines and added no prose", got)
	}
	// The silence above is the rule declining to fire, not a fixture that never moved: this is
	// a rise a ratio comparison does fail, and the paired test below proves it still fails one.
	if now := 2212.0 / 5569; now <= base.Ratio {
		t.Fatalf("fixture: %.4f -> %.4f is not a rise, so it no longer poses the question", base.Ratio, now)
	}
}

// THE ANTI-VACUITY PAIR: the same package, padded instead of trimmed. A rule that passed the
// deletion by passing everything would be decoration, and this is what refuses it.
func TestRatchetFailsProseAddedBesideNoCode(t *testing.T) {
	got := ratchet(t, anchor(3635, 9846), 3835, 10046)

	if len(got) != 1 || got[0].Kind != "regression" {
		t.Fatalf("violations = %v, want one: 200 comment lines arrived beside no code at all", got)
	}
}

// DELETION IS NOT AN AMNESTY, and this is the hole a naive deletion allowance would open. A
// lane that removes 3,000 lines of code and writes 40 comment lines has still written them:
// a shrinking package earns no entitlement, so the 40 stand on their own and fail.
func TestRatchetFailsProseAddedBesideADeletion(t *testing.T) {
	got := ratchet(t, anchor(3635, 9846), 3675, 6886)

	if len(got) != 1 {
		t.Fatalf("violations = %v, want one: the deletion does not pay for the prose beside it", got)
	}
}

// Prose at the package's own density leaves it exactly as dense as it was, which is the most
// any lane is entitled to and is therefore the boundary the check must sit on rather than past.
func TestRatchetPassesProseInProportionToTheCode(t *testing.T) {
	if got := ratchet(t, anchor(200, 1000), 225, 1125); len(got) != 0 {
		t.Fatalf("violations = %v, want none: 25 comment lines beside 100 others is the package's own rate", got)
	}
}

// Its pair, one line past, so the boundary above is the check's edge and not a fixture sitting
// comfortably inside it.
func TestRatchetFailsProseOneLinePastWhatTheCodeCarries(t *testing.T) {
	if got := ratchet(t, anchor(200, 1000), 226, 1126); len(got) != 1 {
		t.Fatalf("violations = %v, want one: 26 comment lines beside 100 others is past the rate", got)
	}
}

// §6 asks for a sweep to REWRITE rather than blank-delete, so shortening a block replaces
// comment lines with fewer comment lines. Counting only what arrived would read that as pure
// prose against no code and refuse the very act the rule prescribes.
func TestRatchetPassesARewriteThatShortensProse(t *testing.T) {
	if got := ratchet(t, anchor(200, 1000), 194, 994); len(got) != 0 {
		t.Fatalf("violations = %v, want none: this lane replaced an eight-line block with two", got)
	}
}

// THE CUT IS A MINIMUM, NOT AN ESTIMATE. A count that overshoots sends a lane into live
// invariants; one that undershoots fails it again after it complied. Cutting a comment line
// takes a total line with it, so both sides move — the arithmetic that forgets this reports a
// number no lane can hit.
func TestRatchetCutCountIsExactlyWhatClearsIt(t *testing.T) {
	base := anchor(200, 1000)

	cut := int(math.Ceil(ProseOverage(base, pkg("p", 260, 1160, 0), 0)))
	if cut != 35 {
		t.Fatalf("cut = %d, want 35: 60 comment lines arrived beside 160 others, of which the "+
			"package's own rate carries 25", cut)
	}
	if got := ratchet(t, base, 260-cut, 1160-cut); len(got) != 0 {
		t.Fatalf("violations = %v after cutting the %d lines the report asked for", got, cut)
	}
	if got := ratchet(t, base, 260-(cut-1), 1160-(cut-1)); len(got) != 1 {
		t.Fatalf("violations = %v one line short of the reported cut, want one: the count is not "+
			"the minimum, so it is asking for prose the rule does not need", got)
	}
}

// THE CURRENCY IS NET COMMENT LINES, and this is its edge — chosen, not overlooked. Prose a
// lane removes pays for prose it adds, because that is what a rewrite IS and §6 asks for
// rewrites over blank deletions. So a lane that deletes commented code does buy room to write:
// bounded by what it removed, bounded again by the per-file ceiling, but real. The tree's prose
// can then only ever grow in proportion to the code carrying it, which is the property the
// ratio comparison never actually guaranteed.
func TestRatchetPassesProseTradedAgainstMoreProseRemoved(t *testing.T) {
	if got := ratchet(t, anchor(3635, 9846), 3335, 8546); len(got) != 0 {
		t.Fatalf("violations = %v, want none: 500 comment lines left with the code they "+
			"documented and 200 arrived, so the package carries less prose than it did", got)
	}
}

// The far side of that edge, and the thing no lane may do: leave a package carrying MORE prose
// than it found there without code to carry it, however much it deleted beside.
func TestRatchetFailsProseThatOutlastsWhatItReplaced(t *testing.T) {
	got := ratchet(t, anchor(3635, 9846), 3735, 8946)

	if len(got) != 1 {
		t.Fatalf("violations = %v, want one: the same removal, but 600 lines arrived where 500 "+
			"left, and the 100 over stand on their own", got)
	}
}

// A rate is a fraction, so an allowance rarely lands on a whole line. Reporting the truncated
// cut asks for one line fewer than clears the check, and a lane that complies is failed again
// for having done exactly what it was told.
func TestRatchetCutRoundsUpToAWholeLine(t *testing.T) {
	base := anchor(100, 500)

	over := ProseOverage(base, pkg("p", 110, 520, 0), 0)
	if over != 7.5 {
		t.Fatalf("overage = %v, want 7.5: the fixture no longer lands between two lines", over)
	}
	if got := ratchet(t, base, 110-8, 520-8); len(got) != 0 {
		t.Fatalf("violations = %v after cutting the 8 lines a rounded-up report asks for", got)
	}
	if got := ratchet(t, base, 110-7, 520-7); len(got) != 1 {
		t.Fatalf("violations = %v after cutting 7, want one: truncating the report would have "+
			"sent a lane to cut seven and fail anyway", got)
	}
}

// The cut a lane acts on is the one in the REPORT. Computing it correctly and then printing
// something else helps nobody, and the arithmetic beside it is what lets a reader check the
// figure rather than take it on trust.
func TestRatchetReportNamesTheCutAndTheCountsBehindIt(t *testing.T) {
	got := ratchet(t, anchor(100, 500), 110, 520)

	if len(got) != 1 {
		t.Fatalf("violations = %v, want one", got)
	}
	const want = "now 110/520: +10 comment line(s) beside +10 other; cut 8"
	if !strings.Contains(got[0].Detail, want) {
		t.Fatalf("detail = %q, want %q in it", got[0].Detail, want)
	}
}

// An anchor carrying a ratio but no counts cannot divide, and the answer must be refusal
// rather than a panic or a silent pass.
func TestRatchetRefusesAddedProseAgainstACountlessAnchor(t *testing.T) {
	if got := ratchet(t, BaselineEntry{Ratio: 0.5}, 8, 10); len(got) != 1 {
		t.Fatalf("violations = %v, want one", got)
	}
}

// --- the same two verdicts through the real git derivation ---

// mainWithADenseAndALeanFile builds a throwaway repo whose one package holds a file well over
// the per-file ceiling and a lean one beside it, and leaves the caller on a lane branch cut
// from it. The lean file is what a lane can delete to raise the package's ratio without
// writing anything.
func mainWithADenseAndALeanFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir)
	writeGoFile(t, dir, "pkg/dense.go", essayLines)
	writeGoFile(t, dir, "pkg/lean.go", leanLines)
	gitMust(t, dir, "add", "-A")
	gitMust(t, dir, "commit", "-m", "baseline on main")
	gitMust(t, dir, "checkout", "-b", "lane")
	return dir
}

// gateVerdict drives what the gate actually runs: changed paths against a real merge-base, the
// main-side census those paths are anchored to, then the standing policy. Reaching through the
// real derivation is the point — a verdict assembled from hand-built counts would agree with
// itself whatever git reported.
func gateVerdict(t *testing.T, dir string) (map[string]*PkgStat, *Baseline, []Violation) {
	t.Helper()
	pkgs, err := Scan(dir)
	if err != nil {
		t.Fatalf("scanning the fixture: %v", err)
	}
	paths := changedPaths(t, dir)
	touched := packageDirs(paths)
	bl := effectiveBaseline(t, dir, &Baseline{Packages: map[string]BaselineEntry{}}, touched)
	return pkgs, bl, scopedGateViolations(pkgs, bl, touched, censusableGoFiles(paths))
}

// End to end, the deletion case: the lane removes the lean file and writes nothing.
func TestGateRatchetPassesALaneThatOnlyDeletes(t *testing.T) {
	dir := mainWithADenseAndALeanFile(t)
	if err := os.Remove(filepath.Join(dir, "pkg", "lean.go")); err != nil {
		t.Fatalf("deleting the lean file: %v", err)
	}

	pkgs, bl, got := gateVerdict(t, dir)

	if len(got) != 0 {
		t.Fatalf("violations = %v, want none: the lane deleted a file and added no line of prose", got)
	}
	if now, was := pkgs["pkg"].Ratio(), bl.Packages["pkg"].Ratio; now <= was {
		t.Fatalf("fixture: ratio %.4f -> %.4f did not rise, so a pass proves nothing", was, now)
	}
}

// End to end, the padding case, on the SAME deletion so the two differ only in whether the
// lane wrote prose. A deletion large enough to move the ratio must not launder the lines
// written beside it.
func TestGateRatchetFailsALaneThatPadsWhileItDeletes(t *testing.T) {
	dir := mainWithADenseAndALeanFile(t)
	if err := os.Remove(filepath.Join(dir, "pkg", "lean.go")); err != nil {
		t.Fatalf("deleting the lean file: %v", err)
	}
	writeGoFile(t, dir, "pkg/dense.go", essayLines+10)

	_, _, got := gateVerdict(t, dir)

	var regressions int
	for _, v := range got {
		if v.Kind == "regression" {
			regressions++
		}
	}
	if regressions != 1 {
		t.Fatalf("violations = %v, want one regression among them: ten comment lines were added "+
			"to a package this lane also shrank", got)
	}
}
