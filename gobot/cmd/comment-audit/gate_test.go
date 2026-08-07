package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// moduleRoot is the tree the gate censuses: the directory holding go.mod, found
// by walking up from this package. All Go source in the repository lives in
// this one module, so this is the whole tree the baseline governs.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %q: the gate cannot census a tree it cannot find", dir)
		}
		dir = parent
	}
}

// TestCommentDensityGate is the enforcement point for ENGINEERING.md §6.
//
// The self-check a lane runs is scoped to the packages it says it touched and is
// only as good as that claim; the gate runs `go test ./...` and nothing else, so
// a failing test is the only thing that can actually stop a merge. This is that
// test. It censuses the whole module but holds the lane to the packages this
// working tree changed against main, which it derives from git rather than
// taking on trust.
//
// THE SCOPE IS THE RULE, NOT A CONCESSION (§6a). The check is a ratchet: nobody
// may leave a package denser than they found it, and nobody answers for density
// they inherited. Applied tree-wide it is no longer a ratchet but a bar, and one
// offending file anywhere then blocks every lane — including the sweep that
// would remove it, which is the deadlock that leaves the tree dense for good.
//
// Within that scope it fails on two conditions, which answer different
// questions:
//
//   - a package denser than the baseline recorded for it — the ratchet.
//   - a file over the per-file ceiling — a bar. The ratchet averages over a
//     whole package, so one file that is mostly essay inside a large package
//     never moves the package number enough to fire.
func TestCommentDensityGate(t *testing.T) {
	root := moduleRoot(t)

	pkgs, err := Scan(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	assertScanIsAimedAtTheTree(t, root, pkgs)

	bl, err := LoadBaseline(filepath.Join(root, ".comment-baseline.json"))
	if err != nil {
		t.Fatalf("loading the baseline: %v\n"+
			"Without it there is nothing to compare against, and a gate that passes "+
			"because it could not run is worse than no gate.", err)
	}
	if len(bl.Packages) == 0 {
		t.Fatal("the baseline records no packages, so every regression would read as clean")
	}

	touched := changedPackages(t, root)
	assertTouchedPathsMatchTheCensus(t, root, touched, pkgs)

	violations := scopedGateViolations(pkgs, effectiveBaseline(t, root, bl, touched), touched)
	if len(violations) == 0 {
		return
	}
	t.Fatal(gateReport(touched, violations))
}

// effectiveBaseline re-anchors each touched package to the density it ACTUALLY has on main,
// leaving every untouched package on its recorded entry.
//
// THE ANCHOR IS MAIN, NOT THE RECORDED NUMBER, AND THAT IS WHAT MAKES THIS A RATCHET. Anchored to
// a frozen baseline the gate fails a lane that IMPROVES a package which drifted past it since —
// the lane inherits every comment line other merges added, and the first lane to touch a drifted
// package pays for all of them. That is a bar, not a ratchet, and it is the deadlock §6a's scoping
// already refuses one layer up. Anchored to main the direction still only goes one way, because a
// lane that leaves a package denser than it found it is refused; main can never climb.
//
// A package absent from main is NEW: it keeps its recorded entry, or none, and the per-file
// ceiling is what answers for it.
func effectiveBaseline(t *testing.T, moduleDir string, bl *Baseline, touched []string) *Baseline {
	t.Helper()
	onMain := mainPackageStats(t, moduleDir, touched)

	out := &Baseline{Packages: make(map[string]BaselineEntry, len(bl.Packages))}
	for k, v := range bl.Packages {
		out.Packages[k] = v
	}
	for _, pkg := range touched {
		p, ok := onMain[pkg]
		if !ok {
			continue
		}
		out.Packages[pkg] = BaselineEntry{Ratio: p.Ratio(), Comment: p.Comment, Total: p.Total, Markers: p.MarkerTotal()}
	}
	return out
}

// mainPackageStats censuses the touched packages as they stand at the merge-base, by checking that
// commit out detached and running the SAME Scan over it. Reusing Scan is the point: a second
// counter written here could disagree with the one the lane is measured by, and the gate would then
// compare two different definitions of a comment.
func mainPackageStats(t *testing.T, moduleDir string, touched []string) map[string]*PkgStat {
	t.Helper()
	if len(touched) == 0 {
		return nil
	}
	base := mergeBaseWithMain(t, moduleDir)

	top, err := runGit(moduleDir, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	rel, err := filepath.Rel(strings.TrimSpace(top), moduleDir)
	if err != nil {
		t.Fatalf("locating the module inside the repository: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "base")
	if _, err := runGit(moduleDir, "worktree", "add", "--detach", "--no-checkout", dir, base); err != nil {
		t.Fatalf("checking out the merge-base to compare against: %v\n"+
			"The gate holds a lane to the density main already has, and it will not fall back "+
			"to a frozen number that would fail an improving lane.", err)
	}
	t.Cleanup(func() { _, _ = runGit(moduleDir, "worktree", "remove", "--force", dir) })

	// Only the touched packages are materialised; a full checkout of a large tree per gate run is
	// wasted work when the comparison covers a handful of directories.
	paths := make([]string, 0, len(touched))
	for _, pkg := range touched {
		paths = append(paths, filepath.Join(rel, pkg))
	}
	if _, err := runGit(dir, append([]string{"checkout", base, "--"}, paths...)...); err != nil {
		t.Fatalf("materialising the merge-base copies of %v: %v", touched, err)
	}

	pkgs, err := Scan(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("scanning the merge-base tree: %v", err)
	}
	return pkgs
}

// scopedGateViolations applies the standing policy to the packages the lane
// touched, and to nothing else.
//
// THE SCOPE IS EXACT, NOT A PREFIX. touched holds the directories whose files
// changed, and a package nested under one of those is a different package whose
// files did not: descending into it would hold this lane to density it never
// opened. -only is the selector that descends, because an operator naming a
// directory means the subtree; the gate names a changed set and means only it.
//
// AN EMPTY SET MEANS NOTHING TO CHECK, AND MUST RETURN HERE. A scope is a filter
// where empty means "no filter", so passing an empty touched set through to
// Check would restore the tree-wide bar this scope exists to prevent — and
// restore it on exactly the runs that changed no Go source, blocking a lane that
// edited only tests or docs with the state of the whole tree.
func scopedGateViolations(pkgs map[string]*PkgStat, bl *Baseline, touched []string) []Violation {
	if len(touched) == 0 {
		return nil
	}
	return Check(pkgs, GatePolicy(bl, ExactPackages(touched)))
}

// changedPackages is the set of packages this working tree changed relative to
// main — committed, staged, unstaged or untracked, since all four are equally
// this lane's doing and none of them are main's.
//
// The comparison is against the merge-base, not against main's tip: a lane that
// has not rebased must answer for its own commits and not for whatever landed on
// main meanwhile.
func changedPackages(t *testing.T, moduleDir string) []string {
	t.Helper()
	base := mergeBaseWithMain(t, moduleDir)

	// --no-renames so a moved file names the directory it LEFT as well as the one
	// it arrived in; the package it left got shorter and its ratio moved too.
	tracked, err := runGit(moduleDir, "diff", "--name-only", "--no-renames", "--relative", base)
	if err != nil {
		t.Fatalf("listing changed files: %v\n"+
			"The gate scopes itself to the packages this tree changed, and it will not "+
			"fall back to checking nothing.", err)
	}
	untracked, err := runGit(moduleDir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		t.Fatalf("listing untracked files: %v\n"+
			"A new package arrives untracked, so skipping these would let one in unchecked.", err)
	}
	return packageDirs(append(splitLinesNonEmpty(tracked), splitLinesNonEmpty(untracked)...))
}

// mergeBaseWithMain resolves where this branch left main. The local branch is
// tried first because that is what a lane is cut from and rebased onto here;
// origin/main is the fallback for a checkout that has no local main.
func mergeBaseWithMain(t *testing.T, dir string) string {
	t.Helper()
	var tried []string
	for _, ref := range []string{"main", "origin/main"} {
		out, err := runGit(dir, "merge-base", "HEAD", ref)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s (%v)", ref, err))
			continue
		}
		sha := strings.TrimSpace(out)
		if !looksLikeSHA(sha) {
			tried = append(tried, fmt.Sprintf("%s (answered %q, which is not a commit)", ref, sha))
			continue
		}
		return sha
	}
	t.Fatalf("no merge-base with main: %s\n"+
		"The gate holds a lane to the packages it changed against main, and it cannot "+
		"work out which those are without that ref. Fetch main, or run from a checkout "+
		"that has it.", strings.Join(tried, "; "))
	return ""
}

// looksLikeSHA is the calibration on the merge-base: an answer that is not a
// commit id is refused here rather than handed to `git diff` as a revision.
func looksLikeSHA(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// runGit captures stdout and stderr separately so a failure is reported by its
// exit status and its message, not inferred from empty output.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "no message on stderr"
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.String(), nil
}

func splitLinesNonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// packageDirs turns changed paths into the packages whose census could have
// moved. Only non-test .go files qualify, because those are the only files the
// census reads: a Makefile, a fixture or a _test.go cannot change a ratio, and
// scoping the gate to their directories would hold a lane to numbers it did not
// touch. Paths arrive slash-separated from git and stay that way, which is how
// the census keys packages.
func packageDirs(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			continue
		}
		dir := path.Dir(p)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

// assertScanIsAimedAtTheTree fails on a census that ran but read the wrong
// thing. An empty or mis-rooted scan produces no violations, which is
// indistinguishable from a clean tree — the one failure this gate must not
// have. The witness is this package's own source, which is present iff the
// scan is rooted where the baseline's keys are written from.
func assertScanIsAimedAtTheTree(t *testing.T, root string, pkgs map[string]*PkgStat) {
	t.Helper()
	const witness = "cmd/comment-audit"
	p := pkgs[witness]
	if p == nil {
		t.Fatalf("census of %s found no package %q (found %d packages): "+
			"the scan is not rooted where the baseline was written from, "+
			"so a pass would mean nothing", root, witness, len(pkgs))
	}
	for _, f := range p.FileStats {
		if f.Path == witness+"/audit.go" {
			return
		}
	}
	t.Fatalf("census of %s found package %q but not its own source: "+
		"file paths are not relative to the module root", root, witness)
}

// assertTouchedPathsMatchTheCensus fails when git's paths and the census's keys
// are written from different roots.
//
// This is the calibration on the scope, and it is the one a wrong aim fails
// silently without: a touched set of package names the census does not use
// matches nothing, so every run passes and the gate is gone. A directory that no
// longer holds Go source is skipped rather than failed — a lane is allowed to
// delete a package.
func assertTouchedPathsMatchTheCensus(t *testing.T, root string, touched []string, pkgs map[string]*PkgStat) {
	t.Helper()
	for _, dir := range touched {
		if pkgs[dir] != nil || isCensusExcluded(dir) {
			continue
		}
		if !holdsCensusableSource(filepath.Join(root, filepath.FromSlash(dir))) {
			continue
		}
		t.Fatalf("git reports a change in %q, which holds Go source, but the census has no such "+
			"package (it has %d): the two are naming directories differently, so the gate would "+
			"be scoped to nothing and pass whatever the tree looks like", dir, len(pkgs))
	}
}

// isCensusExcluded reports a directory the walk never enters. It reads skipDirs
// rather than restating the list, so a lane that adds Go source under testdata
// is not failed for a root mismatch that does not exist.
func isCensusExcluded(dir string) bool {
	for _, part := range strings.Split(dir, "/") {
		if skipDirs[part] {
			return true
		}
	}
	return false
}

func holdsCensusableSource(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			return true
		}
	}
	return false
}

// gateReport renders the failure an engineer reads when the gate blocks them.
// It names every offender, by how much, and what to do about it — a bare count
// would send them to run the tool again to find out — and it names the scope,
// so a reader can see that these are their own packages and check the list.
func gateReport(touched []string, violations []Violation) string {
	var regressions, files, other []Violation
	for _, v := range violations {
		switch {
		case v.File != "":
			files = append(files, v)
		case v.Kind == "regression":
			regressions = append(regressions, v)
		default:
			other = append(other, v)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "comment density has regressed — see ENGINEERING.md §6 for the keep/cut rule.\n")
	fmt.Fprintf(&b, "Checked the %d package(s) this tree changed against main: %s\n",
		len(touched), strings.Join(touched, ", "))

	if len(regressions) > 0 {
		fmt.Fprintf(&b, "\n%d PACKAGE(S) DENSER THAN THE BASELINE (§6a, the ratchet)\n", len(regressions))
		sort.Slice(regressions, func(i, j int) bool {
			return regressions[i].Ratio-regressions[i].Limit > regressions[j].Ratio-regressions[j].Limit
		})
		for _, v := range regressions {
			fmt.Fprintf(&b, "  %-62s %5.2f%% -> %5.2f%%  (+%.2f pts)  %s\n",
				v.Package, v.Limit*100, v.Ratio*100, (v.Ratio-v.Limit)*100, v.Detail)
		}
		b.WriteString("  Cut the archaeology out of the comments you added: history, bead ids and\n" +
			"  measured numbers all belong in the commit, the bead or docs/retrospectives/.\n" +
			"  Re-recording .comment-baseline.json to clear this is the one move §6a forbids.\n")
	}

	if len(files) > 0 {
		fmt.Fprintf(&b, "\n%d FILE(S) OVER THE PER-FILE CEILING (%.0f%% uncredited comment)\n",
			len(files), DefaultMaxFileProseRatio*100)
		sort.Slice(files, func(i, j int) bool { return files[i].Ratio > files[j].Ratio })
		for _, v := range files {
			fmt.Fprintf(&b, "  %-70s %5.2f%%  %s\n", v.File, v.Ratio*100, v.Detail)
		}
		fmt.Fprintf(&b, "  Counted is comment the doc credit does not cover: everything but the package\n"+
			"  doc and the first %d lines of each exported declaration's godoc. The credit RUNS\n"+
			"  OUT — godoc past that cap counts like any other comment, so a file can fail on\n"+
			"  documentation alone and the lines to cut may be a long doc block. Cut it, or make\n"+
			"  it earn its place as a short WHY.\n", DocAllowance)
	}

	for _, v := range other {
		fmt.Fprintf(&b, "\n  %s\n", v)
	}
	return b.String()
}
