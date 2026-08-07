package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestRoutingServicePytestGate puts the routing service's Python suite behind the merge gate.
//
// captain-gate runs `go build ./...` and `go test ./...` and nothing else, and it is a protected
// path. The routing service is Python, so its 271 tests were outside the gate entirely: sp-wtc47
// subtracted gate fees from projected profit, left the golden expectations stale, and merged red
// because nothing could see it. A Go test that shells out to pytest blocks a merge just as hard as
// a gate change would, without touching cmd/captain-gate — the same move cmd/comment-audit made
// for comment density.
//
// It fails on ANY non-zero pytest exit, which is what makes it answer the failure that motivated
// the bead. A collection error exits 2 and an empty selection exits 5: an assertion-only check
// would read both as "nothing failed" and pass, and a suite that cannot be collected is invisible
// in a pass count rather than red in it.
func TestRoutingServicePytestGate(t *testing.T) {
	service := routingServiceDir(t)
	python := serviceInterpreter(t, service)

	registerSuiteInputs(t, service)

	// tests/ and not the service root: model/tests/ is the market-model pipeline, which needs
	// sqlalchemy from the SEPARATE model venv (.venv-p0) and cannot be collected by this
	// interpreter at all. Pointing the gate at the root would fail every merge on that alone.
	cmd := exec.Command(python, "-m", "pytest", "tests/", "-q")
	cmd.Dir = service
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the routing service's pytest suite is failing, so the tour solver cannot be "+
			"merged:\n$ %s -m pytest tests/ -q  (in %s)\n%s\nexit: %v",
			python, service, out, err)
	}
	assertTestsActuallyRan(t, out)
}

// assertTestsActuallyRan is the calibration on a green run: pytest exited 0, and this checks it
// did so having run something. A pass earned by a suite that collected nothing is the false green
// this whole bead is about, and exit 0 alone does not distinguish the two.
func assertTestsActuallyRan(t *testing.T, out []byte) {
	t.Helper()
	m := regexp.MustCompile(`(\d+) passed`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("pytest exited 0 but reported no passing tests, so the suite ran nothing and this "+
			"gate is protecting nothing:\n%s", out)
	}
	if n, _ := strconv.Atoi(string(m[1])); n == 0 {
		t.Fatalf("pytest reported 0 passed:\n%s", out)
	}
}

// routingServiceDir locates the Python service inside this module, and fails when it is not there.
// A gate that silently skips because it could not find its subject is worse than no gate.
func routingServiceDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %q: cannot locate the routing service", dir)
		}
		dir = parent
	}
	service := filepath.Join(dir, "services", "routing-service")
	if _, err := os.Stat(filepath.Join(service, "conftest.py")); err != nil {
		t.Fatalf("no routing service at %s (%v): the gate cannot run a suite it cannot find, and "+
			"it will not pass because it looked in the wrong place", service, err)
	}
	return service
}

// serviceInterpreter resolves the Python that can run the suite, and FAILS rather than skips when
// there is none.
//
// THE WORKTREE IS WHY THIS IS NOT JUST ./venv. captain-gate runs the gate inside the lane's
// worktree, and venv/ is gitignored — so it is absent from every worktree and present only in the
// repository the worktrees hang off. Resolved as ./venv alone this check would find nothing on
// exactly the runs that gate a merge. The fallback reads the git COMMON dir, which is how
// ProvisionWorktree already finds the same repository for the same reason.
//
// A MISSING VENV IS A FAILURE, NOT A SKIP, AND THE CHOICE IS DELIBERATE. A skip is this repo's
// documented disease (sp-4bgic): the check stops protecting silently, and it stops in exactly the
// degraded environments where something is most likely wrong. The usual objection — that a missing
// optional toolchain then blocks every merge — does not hold here, because the venv this resolves
// is the routing service's OWN runtime, not an optional extra. If it is absent the service cannot
// run at all on this machine, and blocking a merge that changes it is correct. The chain above
// makes a false failure unlikely; the message below makes a real one fixable in one command.
func serviceInterpreter(t *testing.T, service string) string {
	t.Helper()
	candidates := []string{filepath.Join(service, "venv", "bin", "python3")}
	if root := repoRootOf(service); root != "" {
		candidates = append(candidates, filepath.Join(root, "gobot", "services", "routing-service", "venv", "bin", "python3"))
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	t.Fatalf("no routing-service venv found, so the Python suite cannot be gated. Tried:\n  %s\n"+
		"This is a failure and not a skip on purpose: a gate that quietly stops checking is how "+
		"the untested merge this test exists to prevent happened. Create it with:\n"+
		"  python3 -m venv %s/venv\n"+
		"  %s/venv/bin/pip install -r %s/requirements.txt -r %s/requirements-model.txt",
		strings.Join(candidates, "\n  "), service, service, service, service)
	return ""
}

// repoRootOf returns the directory holding the shared .git, which for a worktree is the repository
// it was cut from rather than the worktree itself. An error means no git and no fallback, which
// the caller reports as part of the paths it tried.
func repoRootOf(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	common := strings.TrimSpace(string(out))
	if common == "" {
		return ""
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return filepath.Dir(common)
}

// registerSuiteInputs reads every file the pytest run depends on, so that Go's test cache treats
// them as inputs to this test.
//
// WITHOUT IT THIS GATE GOES STALE GREEN, WHICH IS THE FAILURE IT EXISTS TO PREVENT. captain-gate
// runs `go test ./...` with no -count=1, so results are cached. The cache is keyed on what the
// TEST PROCESS touched, and a subprocess's reads are invisible to it — so a lane that changed only
// Python would replay this test's last PASS without running pytest at all. Reading the sources
// here puts them in the testlog, and any edit to them invalidates the entry.
func registerSuiteInputs(t *testing.T, service string) {
	t.Helper()
	skip := map[string]bool{"venv": true, ".venv-p0": true, "__pycache__": true, "model_artifacts": true}
	var read int
	err := filepath.WalkDir(service, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".py") && !strings.HasSuffix(path, ".sh") {
			return nil
		}
		if _, err := os.ReadFile(path); err != nil {
			return err
		}
		read++
		return nil
	})
	if err != nil {
		t.Fatalf("reading the suite's sources: %v", err)
	}
	// The proto is an input too: conftest.py regenerates the stubs when it is newer than them.
	_, _ = os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(service)), "pkg", "proto", "routing", "routing.proto"))

	// Calibration: a walk that read nothing would register no inputs and cache forever.
	if read == 0 {
		t.Fatalf("found no Python sources under %s, so nothing was registered with the test cache "+
			"and a pass here could be replayed over changed code", service)
	}
}
