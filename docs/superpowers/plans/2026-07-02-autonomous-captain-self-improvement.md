# Autonomous Captain — Self-Improvement Pipelines Implementation Plan (2 of 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the gated fix pipeline (bug report → worktree → `claude -p` fix session → build+test gate → squash-merge → daemon restart) and the meta-game improvement loop (daily meta-review + feature building) — spec rollout phases 3–4.

**Architecture:** The supervisor (from plan 1 of 2) gains a fixer subsystem: it scans `captain/reports/bugs/` for reports whose frontmatter says `status: new`, runs a dedicated fix session in a git worktree, gates the result with `go build && go test` executed by the supervisor itself (never trusted from session output), and — only when `captain.auto_merge` is true — squash-merges, rebuilds, and restarts the daemon. A daily meta-review session maintains `state/improvement-backlog.md`; proposals it marks ready ship through the same pipeline with tighter budgets.

**Tech Stack:** Go 1.25, git worktrees, testify. Same runtime constraints as plan 1.

**Prerequisite:** Plan 1 (`2026-07-02-autonomous-captain-core.md`) fully implemented and validated. This plan extends `gobot/internal/captain` (package `captainsup`) and `gobot/cmd/captain`.

**Spec:** `docs/superpowers/specs/2026-07-02-autonomous-captain-design.md` (sections: Self-improvement pipeline, Meta-game improvement loop).

## Global Constraints

- All plan-1 global constraints apply (module path, test style, no new logging deps, no `ANTHROPIC_API_KEY`).
- The gate is ALWAYS run by the supervisor via `exec` — never parse session output to decide pass/fail.
- `captain.auto_merge` defaults to **false** (propose-only). Merging/restarting only happens when it is explicitly true in config.
- Kill switch: `captain/DISABLED_FIXES` stops fix + feature sessions; `captain/DISABLED` stops everything (already implemented in plan 1).
- Fix sessions get a longer timeout (`fix_session_timeout_minutes`, default 30) than strategy sessions.
- Budgets: `max_fixes_per_day` default 3, `max_features_per_day` default 1, feature diff cap `max_feature_diff_lines` default 400.
- Worktrees live under the scratch dir `.captain-worktrees/` at the gobot repo root (gitignored); branches are named `captain/fix-<slug>` / `captain/feat-<slug>`.

---

### Task 1: Config additions for the pipelines

**Files:**
- Modify: `gobot/internal/infrastructure/config/captain.go`
- Modify: `gobot/internal/infrastructure/config/defaults.go`
- Modify: `gobot/internal/infrastructure/config/captain_test.go`
- Modify: `gobot/config.yaml.example`

**Interfaces:**
- Consumes: plan-1 `CaptainConfig`.
- Produces: new fields `AutoMerge bool`, `MaxFixesPerDay int`, `MaxFeaturesPerDay int`, `FixSessionTimeoutMinutes int`, `MaxFeatureDiffLines int`, `RestartCmd string`, `RepoDir string`.

- [ ] **Step 1: Extend the test**

Append to `TestCaptainDefaults` in `gobot/internal/infrastructure/config/captain_test.go`:

```go
	require.False(t, cfg.Captain.AutoMerge)
	require.Equal(t, 3, cfg.Captain.MaxFixesPerDay)
	require.Equal(t, 1, cfg.Captain.MaxFeaturesPerDay)
	require.Equal(t, 30, cfg.Captain.FixSessionTimeoutMinutes)
	require.Equal(t, 400, cfg.Captain.MaxFeatureDiffLines)
	require.Equal(t, ".", cfg.Captain.RepoDir)
	require.Equal(t, "make restart-daemon", cfg.Captain.RestartCmd)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd gobot && go test ./internal/infrastructure/config/ -run TestCaptainDefaults -v`
Expected: FAIL — undefined fields

- [ ] **Step 3: Implement**

Add to `CaptainConfig` in `gobot/internal/infrastructure/config/captain.go`:

```go
	// Self-improvement pipeline (plan 2 of 2)
	AutoMerge                bool   `mapstructure:"auto_merge"`
	MaxFixesPerDay           int    `mapstructure:"max_fixes_per_day" validate:"omitempty,min=1"`
	MaxFeaturesPerDay        int    `mapstructure:"max_features_per_day" validate:"omitempty,min=1"`
	FixSessionTimeoutMinutes int    `mapstructure:"fix_session_timeout_minutes" validate:"omitempty,min=1"`
	MaxFeatureDiffLines      int    `mapstructure:"max_feature_diff_lines" validate:"omitempty,min=10"`
	RepoDir                  string `mapstructure:"repo_dir"`
	RestartCmd               string `mapstructure:"restart_cmd"`
```

Add to `defaults.go` in the captain block:

```go
	if cfg.Captain.MaxFixesPerDay == 0 {
		cfg.Captain.MaxFixesPerDay = 3
	}
	if cfg.Captain.MaxFeaturesPerDay == 0 {
		cfg.Captain.MaxFeaturesPerDay = 1
	}
	if cfg.Captain.FixSessionTimeoutMinutes == 0 {
		cfg.Captain.FixSessionTimeoutMinutes = 30
	}
	if cfg.Captain.MaxFeatureDiffLines == 0 {
		cfg.Captain.MaxFeatureDiffLines = 400
	}
	if cfg.Captain.RepoDir == "" {
		cfg.Captain.RepoDir = "."
	}
	if cfg.Captain.RestartCmd == "" {
		cfg.Captain.RestartCmd = "make restart-daemon"
	}
```

Append to the `captain:` block in `gobot/config.yaml.example`:

```yaml
  # Self-improvement pipeline (phases 3-4; see DISABLED_FIXES kill switch)
  auto_merge: false            # true = merge+restart after gate passes; false = leave branch for human
  max_fixes_per_day: 3
  max_features_per_day: 1
  fix_session_timeout_minutes: 30
  max_feature_diff_lines: 400
  repo_dir: .                  # gobot repo root (worktrees are created under it)
  restart_cmd: make restart-daemon
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd gobot && go test ./internal/infrastructure/config/ -run TestCaptainDefaults -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/infrastructure/config/captain.go gobot/internal/infrastructure/config/captain_test.go gobot/internal/infrastructure/config/defaults.go gobot/config.yaml.example
git commit -m "feat(config): captain self-improvement pipeline settings"
```

---

### Task 2: Bug-report scanner (`reports/bugs/` frontmatter protocol)

**Files:**
- Create: `gobot/internal/captain/bugreports.go`
- Create: `gobot/internal/captain/bugreports_test.go`
- Modify: `captain/CLAUDE.md` (document the frontmatter in the Escalation section)

**Interfaces:**
- Consumes: `Workspace` (plan 1).
- Produces:
  - `captainsup.BugReport` struct: `Path string; Slug string; Title string; Status string; Kind string` (`Kind` is `"fix"` or `"feature"`).
  - `captainsup.ScanReports(dir string) ([]BugReport, error)` — parses YAML-ish frontmatter (`status:`, `kind:`, `title:` lines between `---` fences); reports without frontmatter default to `status: new`, `kind: fix`.
  - `captainsup.SetReportStatus(path, status string) error` — rewrites the status line in place (adds frontmatter if missing).
  - Statuses: `new` → `in_progress` → `merged` | `gate_failed` | `awaiting_human`.

- [ ] **Step 1: Write the failing test**

`gobot/internal/captain/bugreports_test.go`:

```go
package captainsup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const sampleReport = `---
title: Refuel loop crashes on zero-fuel markets
status: new
kind: fix
---

## Failure signature
container command_type=refuel, error class=divide-by-zero
`

func TestScanReportsParsesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-07-02-refuel-crash.md"), []byte(sampleReport), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "no-frontmatter.md"), []byte("just prose"), 0o644))

	reports, err := ScanReports(dir)
	require.NoError(t, err)
	require.Len(t, reports, 2)

	byName := map[string]BugReport{}
	for _, r := range reports {
		byName[filepath.Base(r.Path)] = r
	}
	r := byName["2026-07-02-refuel-crash.md"]
	require.Equal(t, "new", r.Status)
	require.Equal(t, "fix", r.Kind)
	require.Equal(t, "Refuel loop crashes on zero-fuel markets", r.Title)
	require.Equal(t, "2026-07-02-refuel-crash", r.Slug)

	require.Equal(t, "new", byName["no-frontmatter.md"].Status, "missing frontmatter defaults to new")
	require.Equal(t, "fix", byName["no-frontmatter.md"].Kind)
}

func TestSetReportStatusRewritesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.md")
	require.NoError(t, os.WriteFile(path, []byte(sampleReport), 0o644))

	require.NoError(t, SetReportStatus(path, "in_progress"))

	reports, err := ScanReports(dir)
	require.NoError(t, err)
	require.Equal(t, "in_progress", reports[0].Status)
	data, _ := os.ReadFile(path)
	require.Contains(t, string(data), "## Failure signature", "body must be preserved")
}

func TestScanReportsMissingDirIsEmpty(t *testing.T) {
	reports, err := ScanReports(filepath.Join(t.TempDir(), "nope"))
	require.NoError(t, err)
	require.Empty(t, reports)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd gobot && go test ./internal/captain/ -run "TestScanReports|TestSetReportStatus" -v`
Expected: FAIL — `undefined: ScanReports`

- [ ] **Step 3: Implement bugreports.go**

`gobot/internal/captain/bugreports.go`:

```go
package captainsup

import (
	"os"
	"path/filepath"
	"strings"
)

type BugReport struct {
	Path   string
	Slug   string
	Title  string
	Status string // new | in_progress | merged | gate_failed | awaiting_human
	Kind   string // fix | feature
}

// ScanReports reads every .md file in dir and parses its frontmatter.
// Files without frontmatter are treated as {status: new, kind: fix} so a
// hastily written report still enters the pipeline.
func ScanReports(dir string) ([]BugReport, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []BugReport
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		r := BugReport{
			Path:   path,
			Slug:   strings.TrimSuffix(e.Name(), ".md"),
			Status: "new",
			Kind:   "fix",
		}
		parseFrontmatter(string(data), &r)
		out = append(out, r)
	}
	return out, nil
}

func parseFrontmatter(content string, r *BugReport) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			return
		}
		key, val, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "status":
			r.Status = val
		case "kind":
			r.Kind = val
		case "title":
			r.Title = val
		}
	}
}

// SetReportStatus rewrites (or inserts) the status field, preserving the body.
func SetReportStatus(path, status string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		replaced := false
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				if !replaced {
					lines = append(lines[:i], append([]string{"status: " + status}, lines[i:]...)...)
				}
				break
			}
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "status:") {
				lines[i] = "status: " + status
				replaced = true
			}
		}
		content = strings.Join(lines, "\n")
	} else {
		content = "---\nstatus: " + status + "\nkind: fix\n---\n" + content
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd gobot && go test ./internal/captain/ -run "TestScanReports|TestSetReportStatus" -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Document the frontmatter in the workspace**

In `captain/CLAUDE.md`, replace the Escalation section's report-format sentence ("Write `reports/bugs/YYYY-MM-DD-<slug>.md` containing: ...") with:

```markdown
STOP retrying. Write `reports/bugs/YYYY-MM-DD-<slug>.md` starting with EXACTLY
this frontmatter:

    ---
    title: <one-line summary>
    status: new
    kind: fix
    ---

followed by: failure signature, evidence (container ids, log excerpts),
expected vs actual behavior, impact, and — if you have one — a suspected root
cause. The fix pipeline picks up `status: new` reports automatically.
```

- [ ] **Step 6: Commit**

```bash
git add gobot/internal/captain/bugreports.go gobot/internal/captain/bugreports_test.go captain/CLAUDE.md
git commit -m "feat(captain): bug report scanner with frontmatter status protocol"
```

---

### Task 3: Git worktree manager + gate runner

**Files:**
- Create: `gobot/internal/captain/worktree.go`
- Create: `gobot/internal/captain/worktree_test.go`
- Modify: `gobot/.gitignore` (add `.captain-worktrees/`)

**Interfaces:**
- Consumes: `os/exec` git; nothing captain-specific.
- Produces:
  - `captainsup.Worktree` struct: `Dir string; Branch string`.
  - `captainsup.CreateWorktree(repoDir, branch string) (Worktree, error)` — `git worktree add -b <branch> <repoDir>/.captain-worktrees/<branch-slug> HEAD`.
  - `(Worktree) Remove(repoDir string) error` — `git worktree remove --force` + `git branch -D`.
  - `captainsup.RunGate(dir string, timeout time.Duration) (pass bool, output string)` — runs `go build ./...` then `go test ./...` in dir; pass only if both exit 0.
  - `captainsup.DiffLines(repoDir, branch string) (int, error)` — total changed lines of branch vs HEAD via `git diff --shortstat HEAD..<branch>` parsing insertions+deletions.
  - `captainsup.SquashMerge(repoDir, branch, message string) error` — `git merge --squash <branch>` + `git commit -m <message>` in repoDir; returns error on conflict (caller leaves branch for human).

- [ ] **Step 1: Write the failing test (scratch git repo fixture)**

`gobot/internal/captain/worktree_test.go`:

```go
package captainsup

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func initScratchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scratch\n\ngo 1.25\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644))
	run("add", "-A")
	run("commit", "-m", "init")
	return dir
}

func TestWorktreeCreateModifyGateMerge(t *testing.T) {
	repo := initScratchRepo(t)

	wt, err := CreateWorktree(repo, "captain/fix-test")
	require.NoError(t, err)
	require.DirExists(t, wt.Dir)

	// Simulate a fix session editing code in the worktree.
	require.NoError(t, os.WriteFile(filepath.Join(wt.Dir, "fix.go"),
		[]byte("package main\n\n// Fixed is the fix.\nfunc Fixed() bool { return true }\n"), 0o644))
	runGit(t, wt.Dir, "add", "-A")
	runGit(t, wt.Dir, "commit", "-m", "fix")

	pass, output := RunGate(wt.Dir, time.Minute)
	require.True(t, pass, "gate output: %s", output)

	lines, err := DiffLines(repo, "captain/fix-test")
	require.NoError(t, err)
	require.Greater(t, lines, 0)

	require.NoError(t, SquashMerge(repo, "captain/fix-test", "fix: test"))
	require.FileExists(t, filepath.Join(repo, "fix.go"))

	require.NoError(t, wt.Remove(repo))
	require.NoDirExists(t, wt.Dir)
}

func TestRunGateFailsOnBrokenCode(t *testing.T) {
	repo := initScratchRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "broken.go"),
		[]byte("package main\n\nfunc broken() { undefinedCall() }\n"), 0o644))

	pass, output := RunGate(repo, time.Minute)
	require.False(t, pass)
	require.Contains(t, output, "undefinedCall")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd gobot && go test ./internal/captain/ -run "TestWorktree|TestRunGate" -v`
Expected: FAIL — `undefined: CreateWorktree`

- [ ] **Step 3: Implement worktree.go**

`gobot/internal/captain/worktree.go`:

```go
package captainsup

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const worktreeRoot = ".captain-worktrees"

type Worktree struct {
	Dir    string
	Branch string
}

func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

func CreateWorktree(repoDir, branch string) (Worktree, error) {
	slug := strings.ReplaceAll(branch, "/", "-")
	dir := filepath.Join(repoDir, worktreeRoot, slug)
	if _, err := gitRun(repoDir, "worktree", "add", "-b", branch, dir, "HEAD"); err != nil {
		return Worktree{}, err
	}
	return Worktree{Dir: dir, Branch: branch}, nil
}

func (w Worktree) Remove(repoDir string) error {
	if _, err := gitRun(repoDir, "worktree", "remove", "--force", w.Dir); err != nil {
		return err
	}
	_, err := gitRun(repoDir, "branch", "-D", w.Branch)
	return err
}

// RunGate builds and tests the tree. The supervisor runs this itself; the fix
// session's claims are never trusted (spec: Self-improvement §3).
func RunGate(dir string, timeout time.Duration) (bool, string) {
	deadline := time.Now().Add(timeout)
	var combined strings.Builder
	for _, args := range [][]string{{"build", "./..."}, {"test", "./..."}} {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, combined.String() + "\ngate timeout"
		}
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		done := make(chan struct{})
		var out []byte
		var err error
		go func() { out, err = cmd.CombinedOutput(); close(done) }()
		select {
		case <-done:
		case <-time.After(remaining):
			_ = cmd.Process.Kill()
			return false, combined.String() + "\ngate timeout during go " + args[0]
		}
		combined.WriteString(fmt.Sprintf("$ go %s\n%s\n", strings.Join(args, " "), out))
		if err != nil {
			return false, combined.String()
		}
	}
	return true, combined.String()
}

var shortstatRe = regexp.MustCompile(`(\d+) insertion|(\d+) deletion`)

// DiffLines counts changed lines on branch relative to current HEAD.
func DiffLines(repoDir, branch string) (int, error) {
	out, err := gitRun(repoDir, "diff", "--shortstat", "HEAD.."+branch)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, m := range shortstatRe.FindAllStringSubmatch(out, -1) {
		for _, g := range m[1:] {
			if g != "" {
				n, _ := strconv.Atoi(g)
				total += n
			}
		}
	}
	return total, nil
}

func SquashMerge(repoDir, branch, message string) error {
	if _, err := gitRun(repoDir, "merge", "--squash", branch); err != nil {
		_, _ = gitRun(repoDir, "merge", "--abort")
		return err
	}
	_, err := gitRun(repoDir, "commit", "-m", message)
	return err
}
```

Add `.captain-worktrees/` on its own line to `gobot/.gitignore`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd gobot && go test ./internal/captain/ -run "TestWorktree|TestRunGate" -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
git add gobot/internal/captain/worktree.go gobot/internal/captain/worktree_test.go gobot/.gitignore
git commit -m "feat(captain): worktree manager, supervisor-run gate, squash-merge helpers"
```

---

### Task 4: Fix pipeline orchestrator

**Files:**
- Create: `gobot/internal/captain/fixer.go`
- Create: `gobot/internal/captain/fixer_test.go`
- Modify: `gobot/Makefile` (add `restart-daemon` target)

**Interfaces:**
- Consumes: `ScanReports`/`SetReportStatus` (Task 2), `CreateWorktree`/`RunGate`/`DiffLines`/`SquashMerge` (Task 3), `SessionRunner` (plan 1).
- Produces:
  - `captainsup.Fixer` struct via `NewFixer(ws Workspace, runner SessionRunner, cfg config.CaptainConfig) *Fixer`. The `runner` here is a SECOND `ClaudeRunner` whose WorkDir is the worktree — but since WorkDir is fixed at construction, `Fixer` takes a `RunnerFactory func(workDir string) SessionRunner` instead.
  - `(f *Fixer) ProcessOne(ctx context.Context, now time.Time) (acted bool, err error)` — finds the oldest `status: new` report, honors budgets/kill switch, runs the pipeline, updates the report status. One report per call (the supervisor calls it once per tick).
  - `captainsup.FixPrompt(report BugReport, body string) string` — the fix-session prompt template.
  - Daily budget tracking is in-memory (`fixStarts []time.Time` field, pruned to the last 24h), the same pattern as plan 1's hourly session cap.

- [ ] **Step 1: Write the failing tests**

`gobot/internal/captain/fixer_test.go`:

```go
package captainsup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// fixStubRunner simulates a fix session by writing a file in the worktree.
type fixStubRunner struct {
	workDir string
	write   string // file content to write as fix.go; empty = write nothing
	broken  bool   // write uncompilable code instead
	prompts []string
}

func (f *fixStubRunner) Run(_ context.Context, prompt string) error {
	f.prompts = append(f.prompts, prompt)
	content := f.write
	if f.broken {
		content = "package main\n\nfunc bad() { nope() }\n"
	}
	if content == "" {
		return nil
	}
	if err := os.WriteFile(filepath.Join(f.workDir, "fix.go"), []byte(content), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = f.workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		panic(string(out))
	}
	cmd = exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "fix")
	cmd.Dir = f.workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		panic(string(out))
	}
	return nil
}

func newFixerFixture(t *testing.T, stub *fixStubRunner, autoMerge bool) (*Fixer, string, string) {
	t.Helper()
	repo := initScratchRepo(t)
	wsDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "reports", "bugs"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(wsDir, "state"), 0o755))
	reportPath := filepath.Join(wsDir, "reports", "bugs", "2026-07-02-refuel-crash.md")
	require.NoError(t, os.WriteFile(reportPath, []byte(sampleReport), 0o644))

	cfg := config.CaptainConfig{
		PlayerID: 1, WorkspaceDir: wsDir, RepoDir: repo, AutoMerge: autoMerge,
		MaxFixesPerDay: 3, FixSessionTimeoutMinutes: 1, MaxFeatureDiffLines: 400,
		RestartCmd: "true", // no-op shell command for tests
	}
	factory := func(workDir string) SessionRunner { stub.workDir = workDir; return stub }
	return NewFixer(NewWorkspace(wsDir), factory, cfg), reportPath, repo
}

func TestFixPipelineGatePassAutoMerge(t *testing.T) {
	stub := &fixStubRunner{write: "package main\n\n// Fixed.\nfunc Fixed() bool { return true }\n"}
	fixer, reportPath, repo := newFixerFixture(t, stub, true)

	acted, err := fixer.ProcessOne(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, acted)
	require.Len(t, stub.prompts, 1)
	require.Contains(t, stub.prompts[0], "Refuel loop crashes")

	reports, _ := ScanReports(filepath.Dir(reportPath))
	require.Equal(t, "merged", reports[0].Status)
	require.FileExists(t, filepath.Join(repo, "fix.go"), "squash-merged into repo")
}

func TestFixPipelineGateFailLeavesBranch(t *testing.T) {
	stub := &fixStubRunner{broken: true}
	fixer, reportPath, repo := newFixerFixture(t, stub, true)

	acted, err := fixer.ProcessOne(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, acted)

	reports, _ := ScanReports(filepath.Dir(reportPath))
	require.Equal(t, "gate_failed", reports[0].Status)
	require.NoFileExists(t, filepath.Join(repo, "fix.go"))

	out, err := exec.Command("git", "-C", repo, "branch", "--list", "captain/fix-*").Output()
	require.NoError(t, err)
	require.Contains(t, string(out), "captain/fix-2026-07-02-refuel-crash", "branch preserved for human")
}

func TestFixPipelineProposeOnlyMode(t *testing.T) {
	stub := &fixStubRunner{write: "package main\n\n// Fixed.\nfunc Fixed() bool { return true }\n"}
	fixer, reportPath, repo := newFixerFixture(t, stub, false) // auto_merge off

	acted, err := fixer.ProcessOne(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, acted)

	reports, _ := ScanReports(filepath.Dir(reportPath))
	require.Equal(t, "awaiting_human", reports[0].Status)
	require.NoFileExists(t, filepath.Join(repo, "fix.go"), "must not merge in propose-only mode")
}

func TestFixPipelineRespectsKillSwitchAndBudget(t *testing.T) {
	stub := &fixStubRunner{write: "package main\n\nfunc F() {}\n"}
	fixer, _, _ := newFixerFixture(t, stub, true)

	require.NoError(t, os.WriteFile(filepath.Join(fixer.ws.Dir(), "DISABLED_FIXES"), nil, 0o644))
	acted, err := fixer.ProcessOne(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, acted, "kill switch must stop the pipeline")
	require.NoError(t, os.Remove(filepath.Join(fixer.ws.Dir(), "DISABLED_FIXES")))

	now := time.Now()
	fixer.fixStarts = []time.Time{now, now, now} // budget 3/day exhausted
	acted, err = fixer.ProcessOne(context.Background(), now)
	require.NoError(t, err)
	require.False(t, acted, "daily budget must stop the pipeline")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd gobot && go test ./internal/captain/ -run TestFixPipeline -v`
Expected: FAIL — `undefined: Fixer`

- [ ] **Step 3: Implement fixer.go**

`gobot/internal/captain/fixer.go`:

```go
package captainsup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

type RunnerFactory func(workDir string) SessionRunner

// Fixer drives the gated self-improvement pipeline (spec: Self-improvement).
type Fixer struct {
	ws      Workspace
	factory RunnerFactory
	cfg     config.CaptainConfig

	fixStarts []time.Time
}

func NewFixer(ws Workspace, factory RunnerFactory, cfg config.CaptainConfig) *Fixer {
	return &Fixer{ws: ws, factory: factory, cfg: cfg}
}

func (f *Fixer) fixesDisabled() bool {
	_, err := os.Stat(filepath.Join(f.ws.Dir(), "DISABLED_FIXES"))
	return err == nil
}

func (f *Fixer) startsInLastDay(now time.Time) int {
	cutoff := now.Add(-24 * time.Hour)
	kept := f.fixStarts[:0]
	for _, t := range f.fixStarts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	f.fixStarts = kept
	return len(kept)
}

// ProcessOne handles at most one `status: new` report per call.
func (f *Fixer) ProcessOne(ctx context.Context, now time.Time) (bool, error) {
	if f.ws.Disabled() || f.fixesDisabled() {
		return false, nil
	}
	reports, err := ScanReports(filepath.Join(f.ws.Dir(), "reports", "bugs"))
	if err != nil {
		return false, err
	}
	var target *BugReport
	for i := range reports {
		if reports[i].Status == "new" {
			target = &reports[i]
			break
		}
	}
	if target == nil {
		return false, nil
	}

	budget := f.cfg.MaxFixesPerDay
	if target.Kind == "feature" {
		budget = f.cfg.MaxFeaturesPerDay
	}
	if f.startsInLastDay(now) >= budget {
		fmt.Printf("captain fixer: daily budget reached, %s stays queued\n", target.Slug)
		return false, nil
	}

	f.fixStarts = append(f.fixStarts, now)
	if err := SetReportStatus(target.Path, "in_progress"); err != nil {
		return true, err
	}

	prefix := "fix"
	if target.Kind == "feature" {
		prefix = "feat"
	}
	branch := fmt.Sprintf("captain/%s-%s", prefix, target.Slug)
	wt, err := CreateWorktree(f.cfg.RepoDir, branch)
	if err != nil {
		_ = SetReportStatus(target.Path, "new") // retryable
		return true, err
	}

	body, _ := os.ReadFile(target.Path)
	runner := f.factory(wt.Dir)
	timeout := time.Duration(f.cfg.FixSessionTimeoutMinutes) * time.Minute
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := runner.Run(sctx, FixPrompt(*target, string(body))); err != nil {
		_ = SetReportStatus(target.Path, "gate_failed")
		fmt.Printf("captain fixer: session failed for %s: %v (branch %s kept)\n", target.Slug, err, branch)
		return true, nil
	}

	pass, gateOut := RunGate(wt.Dir, timeout)
	if !pass {
		_ = SetReportStatus(target.Path, "gate_failed")
		_ = os.WriteFile(target.Path+".gate.log", []byte(gateOut), 0o644)
		_ = gitCleanWorktreeOnly(f.cfg.RepoDir, wt) // remove worktree dir, KEEP branch
		fmt.Printf("captain fixer: gate FAILED for %s, branch %s left for human\n", target.Slug, branch)
		return true, nil
	}

	if target.Kind == "feature" {
		lines, err := DiffLines(f.cfg.RepoDir, branch)
		if err == nil && lines > f.cfg.MaxFeatureDiffLines {
			_ = SetReportStatus(target.Path, "awaiting_human")
			_ = gitCleanWorktreeOnly(f.cfg.RepoDir, wt)
			fmt.Printf("captain fixer: %s diff too large (%d > %d lines), left for human\n",
				target.Slug, lines, f.cfg.MaxFeatureDiffLines)
			return true, nil
		}
	}

	if !f.cfg.AutoMerge {
		_ = SetReportStatus(target.Path, "awaiting_human")
		_ = gitCleanWorktreeOnly(f.cfg.RepoDir, wt)
		fmt.Printf("captain fixer: gate PASSED for %s; propose-only mode, branch %s awaits review\n",
			target.Slug, branch)
		return true, nil
	}

	msg := fmt.Sprintf("%s(captain): %s\n\nAutomated by the captain fix pipeline. Report: %s",
		prefix, target.Title, filepath.Base(target.Path))
	if err := SquashMerge(f.cfg.RepoDir, branch, msg); err != nil {
		_ = SetReportStatus(target.Path, "awaiting_human")
		return true, fmt.Errorf("squash-merge %s: %w", branch, err)
	}
	_ = wt.Remove(f.cfg.RepoDir)
	_ = SetReportStatus(target.Path, "merged")

	fmt.Printf("captain fixer: %s merged; restarting daemon (%s)\n", target.Slug, f.cfg.RestartCmd)
	restart := exec.CommandContext(ctx, "sh", "-c", f.cfg.RestartCmd)
	restart.Dir = f.cfg.RepoDir
	if out, err := restart.CombinedOutput(); err != nil {
		return true, fmt.Errorf("restart failed: %w: %s", err, out)
	}
	return true, nil
}

// gitCleanWorktreeOnly removes the worktree directory but preserves the branch
// so a human can inspect the attempt.
func gitCleanWorktreeOnly(repoDir string, wt Worktree) error {
	_, err := gitRun(repoDir, "worktree", "remove", "--force", wt.Dir)
	return err
}

// FixPrompt is the dedicated fix-session prompt (spec: Self-improvement §2).
func FixPrompt(r BugReport, body string) string {
	return fmt.Sprintf(`You are an automated repair session for the SpaceTraders gobot, working in an
isolated git worktree on branch captain/fix-%s. Fix ONLY the bug described
below.

Rules (non-negotiable):
- TDD: write a failing test that reproduces the bug FIRST, then the minimal fix.
- Minimal diff. No refactoring, no drive-by changes, no new dependencies.
- Never create or edit files under migrations/. Never touch files outside this
  worktree.
- Commit your work with git when done (conventional commit message).
- The supervisor will run 'go build ./... && go test ./...' itself; if that
  fails, your branch is discarded to a human. Run the tests yourself before
  finishing.

## Bug report
%s`, r.Slug, body)
}
```

- [ ] **Step 4: Add the restart-daemon Makefile target**

In `gobot/Makefile` (used by the default `restart_cmd`):

```makefile
restart-daemon: build-daemon
	@if [ -f /tmp/spacetraders-daemon.pid ]; then \
		kill $$(cat /tmp/spacetraders-daemon.pid) 2>/dev/null || true; \
		sleep 2; \
	fi
	nohup ./bin/spacetraders-daemon >> daemon.log 2>&1 &
	@echo "daemon restarted (log: daemon.log)"
```

Add `restart-daemon` to `.PHONY`. Check the actual PID file path in config (`daemon.pid_file`, default `/tmp/spacetraders-daemon.pid`).

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd gobot && go test ./internal/captain/ -run TestFixPipeline -v`
Expected: PASS (4 tests)

- [ ] **Step 6: Commit**

```bash
git add gobot/internal/captain/fixer.go gobot/internal/captain/fixer_test.go gobot/Makefile
git commit -m "feat(captain): gated fix pipeline (worktree, gate, budgets, propose-only + auto-merge)"
```

---

### Task 5: Meta-review sessions (daily) + friction/backlog wiring

**Files:**
- Create: `gobot/internal/captain/metareview.go`
- Create: `gobot/internal/captain/metareview_test.go`
- Create: `captain/state/improvement-backlog.md`
- Modify: `captain/CLAUDE.md` (meta-review session contract)

**Interfaces:**
- Consumes: `Workspace`, `SessionRunner`, `ReadDecisions` (plan 1); `CurrentCredits` (plan 1).
- Produces:
  - `captainsup.MetaReviewDue(ws Workspace, now time.Time) bool` — true when `state/last-meta-review` file is missing or older than 24h (the file holds an RFC3339 timestamp; supervisor writes it after a successful meta session).
  - `captainsup.MarkMetaReviewDone(ws Workspace, now time.Time) error`.
  - `captainsup.ComposeMetaReview(ctx context.Context, db *gorm.DB, ws Workspace, playerID int, now time.Time) (string, error)` — the meta-review prompt: KPI trend (24h vs previous 24h credits delta), full `lessons.md`, full `improvement-backlog.md`, `friction:` lines grepped from `captain-log.md`, and the meta-review obligations.

- [ ] **Step 1: Write the failing tests**

`gobot/internal/captain/metareview_test.go`:

```go
package captainsup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMetaReviewDueOncePerDay(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	now := time.Now()

	require.True(t, MetaReviewDue(ws, now), "missing marker file means due")
	require.NoError(t, MarkMetaReviewDone(ws, now))
	require.False(t, MetaReviewDue(ws, now.Add(2*time.Hour)))
	require.True(t, MetaReviewDue(ws, now.Add(25*time.Hour)))
}

func TestComposeMetaReviewGathersFrictionAndBacklog(t *testing.T) {
	db, playerID, _ := setupDB(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	require.NoError(t, os.WriteFile(ws.StatePath("captain-log.md"), []byte(
		"## 2026-07-01\ndecided things\nfriction: no arbitrage scan command, chained 3 CLIs by hand\n"+
			"## 2026-07-02\nfriction: snapshot lacks cargo value estimates\nother line\n"), 0o644))
	require.NoError(t, os.WriteFile(ws.StatePath("improvement-backlog.md"),
		[]byte("# Backlog\n- P1: arbitrage scan command"), 0o644))
	require.NoError(t, os.WriteFile(ws.StatePath("lessons.md"), []byte("L1 — probes are cheap"), 0o644))

	prompt, err := ComposeMetaReview(context.Background(), db, ws, playerID, time.Now())
	require.NoError(t, err)

	require.Contains(t, prompt, "no arbitrage scan command")
	require.Contains(t, prompt, "snapshot lacks cargo value estimates")
	require.NotContains(t, prompt, "other line", "only friction: lines are extracted from the log")
	require.Contains(t, prompt, "P1: arbitrage scan command")
	require.Contains(t, prompt, "L1 — probes are cheap")
	require.Contains(t, prompt, "at most ONE proposal")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd gobot && go test ./internal/captain/ -run TestMetaReview -v && go test ./internal/captain/ -run TestComposeMetaReview -v`
Expected: FAIL — `undefined: MetaReviewDue`

- [ ] **Step 3: Implement metareview.go**

`gobot/internal/captain/metareview.go`:

```go
package captainsup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"
)

const metaReviewMarker = "last-meta-review"

func MetaReviewDue(ws Workspace, now time.Time) bool {
	data, err := os.ReadFile(ws.StatePath(metaReviewMarker))
	if err != nil {
		return true
	}
	last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return true
	}
	return now.Sub(last) >= 24*time.Hour
}

func MarkMetaReviewDone(ws Workspace, now time.Time) error {
	return os.WriteFile(ws.StatePath(metaReviewMarker), []byte(now.UTC().Format(time.RFC3339)), 0o644)
}

// frictionLines extracts `friction:` observations from the captain's log.
func frictionLines(log string) []string {
	var out []string
	for _, line := range strings.Split(log, "\n") {
		if idx := strings.Index(strings.ToLower(line), "friction:"); idx >= 0 {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// ComposeMetaReview builds the daily meta-game session prompt
// (spec: Meta-game improvement loop §2).
func ComposeMetaReview(ctx context.Context, db *gorm.DB, ws Workspace, playerID int, now time.Time) (string, error) {
	var b strings.Builder
	b.WriteString("# Meta-review: upgrade your own instrument panel\n")
	b.WriteString("Generated: " + now.UTC().Format(time.RFC3339) + "\n\n")

	credits, err := CurrentCredits(ctx, db, playerID)
	if err != nil {
		return "", err
	}
	b.WriteString(fmt.Sprintf("## KPI check\n- Current credits: %d\n", credits))

	b.WriteString("\n## Friction observed (from your log)\n")
	fl := frictionLines(ws.ReadFull("captain-log.md"))
	if len(fl) == 0 {
		b.WriteString("(none recorded — if that is untrue, your sessions are not logging friction; fix that habit)\n")
	}
	for _, l := range fl {
		b.WriteString("- " + l + "\n")
	}

	b.WriteString("\n## Lessons (state/lessons.md)\n")
	b.WriteString(ws.ReadFull("lessons.md") + "\n")

	b.WriteString("\n## Current improvement backlog (state/improvement-backlog.md)\n")
	b.WriteString(ws.ReadFull("improvement-backlog.md") + "\n")

	b.WriteString(`
## Your obligations this meta-review
1. Rewrite state/improvement-backlog.md: re-score existing proposals against
   the evidence above, prune obsolete ones, add new ones from friction. Each
   proposal needs: problem, evidence (decision/friction refs), sketch of the
   change, expected ROI (credits/hour or captain effectiveness).
2. Promote at most ONE proposal to ready by writing a feature report to
   reports/bugs/YYYY-MM-DD-<slug>.md with frontmatter kind: feature,
   status: new. Only promote when the top proposal's evidence is strong; an
   empty promotion round is a fine outcome.
3. Verify the last merged improvement (if any) actually moved the KPI it
   promised; record the verdict as a lesson in state/lessons.md.
4. Append a meta-review entry to state/captain-log.md.
`)
	return b.String(), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd gobot && go test ./internal/captain/ -run "TestMetaReview|TestComposeMetaReview" -v`
Expected: PASS

- [ ] **Step 5: Seed the backlog file and extend the workspace contract**

`captain/state/improvement-backlog.md`:

```markdown
# Improvement backlog

Maintained by the daily meta-review session. Format per proposal:

## P<n>: <title>
- Problem:
- Evidence: (decision ids, friction log refs)
- Sketch: (new CLI command / snapshot field / workflow change)
- Expected ROI:
- Score: (re-scored every meta-review)
```

Append to `captain/CLAUDE.md`:

```markdown
## Meta-review sessions

Some sessions are meta-reviews (the prompt says so). In those you do NOT trade
or command ships — you upgrade the instrument panel: curate the improvement
backlog, promote at most one proposal to a `kind: feature` report, and verify
whether the last shipped improvement earned its keep.
```

- [ ] **Step 6: Commit**

```bash
git add gobot/internal/captain/metareview.go gobot/internal/captain/metareview_test.go captain/state/improvement-backlog.md captain/CLAUDE.md
git commit -m "feat(captain): daily meta-review session (friction -> ranked backlog -> feature reports)"
```

---

### Task 6: Wire fixer + meta-review into the supervisor and `cmd/captain`

**Files:**
- Modify: `gobot/internal/captain/supervisor.go`
- Modify: `gobot/internal/captain/supervisor_test.go`
- Modify: `gobot/cmd/captain/main.go`

**Interfaces:**
- Consumes: `Fixer.ProcessOne`, `MetaReviewDue`/`MarkMetaReviewDone`/`ComposeMetaReview` (Tasks 4–5).
- Produces: `Supervisor` gains optional collaborators via `SetFixer(f *Fixer)` and a meta-review branch inside `Tick`. Tick priority order: (1) strategy session if events/heartbeat due, else (2) meta-review if due, then (3) one fixer step every tick (independent of session caps — fix sessions have their own daily budget).

- [ ] **Step 1: Write the failing tests**

Append to `gobot/internal/captain/supervisor_test.go`:

```go
func TestTickRunsMetaReviewWhenDueAndIdle(t *testing.T) {
	runner := &stubRunner{}
	sup, _ := newTestSupervisor(t, runner)
	sup.lastSession = time.Now() // no strategy trigger

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Len(t, runner.prompts, 1)
	require.Contains(t, runner.prompts[0], "Meta-review")

	// Immediately after, meta-review is no longer due.
	ran, err = sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.False(t, ran)
	require.Len(t, runner.prompts, 1)
}

func TestTickPrefersStrategyOverMetaReview(t *testing.T) {
	runner := &stubRunner{}
	sup, s := newTestSupervisor(t, runner)
	sup.lastSession = time.Now()
	require.NoError(t, s.store.Record(context.Background(),
		&captain.Event{Type: captain.EventWorkflowFailed, Ship: "S", PlayerID: s.playerID}))

	ran, err := sup.Tick(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, ran)
	require.Contains(t, runner.prompts[0], "Fleet situation report",
		"events outrank the meta-review")
}
```

Note: `newTestSupervisor` (plan 1) creates a fresh workspace with no `last-meta-review` marker, so meta-review is due by default — `TestTickNoTriggerNoSession` from plan 1 will now FAIL. Update it to expect the meta-review instead is wrong — instead, make that test mark meta-review done first:

```go
// In TestTickNoTriggerNoSession, add before the Tick call:
	require.NoError(t, MarkMetaReviewDone(sup.ws, time.Now()))
```

Apply the same `MarkMetaReviewDone` line to `TestTickRespectsHourlyCap` and `TestTickRespectsKillSwitch` (kill switch must win over meta-review — verify the test still passes without the marker too; if it does, leave it unmarked to prove that).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd gobot && go test ./internal/captain/ -run TestTick -v`
Expected: FAIL — new tests fail (`Meta-review` never appears); old tests may fail per the note above.

- [ ] **Step 3: Extend the supervisor**

In `gobot/internal/captain/supervisor.go`:

(a) Add fields to `Supervisor`:

```go
	fixer *Fixer // optional; nil in phase 1-2 deployments
```

(b) Add the setter:

```go
// SetFixer enables the self-improvement pipeline (plan 2 of 2).
func (s *Supervisor) SetFixer(f *Fixer) { s.fixer = f }
```

(c) In `Tick`, replace the early-return `if len(events) == 0 && !heartbeatDue { return false, nil }` with:

```go
	if len(events) == 0 && !heartbeatDue {
		return s.tickSecondary(ctx, now)
	}
```

and after the successful strategy-session block (before the final `return true, nil`), also run the fixer step:

```go
	if s.fixer != nil {
		if _, err := s.fixer.ProcessOne(ctx, now); err != nil {
			fmt.Printf("captain fixer: %v\n", err)
		}
	}
```

(d) Add `tickSecondary`:

```go
// tickSecondary runs when no strategy session is needed: meta-review first,
// then one fixer step. Meta-review respects the same hourly session cap.
func (s *Supervisor) tickSecondary(ctx context.Context, now time.Time) (bool, error) {
	ran := false
	if MetaReviewDue(s.ws, now) && s.sessionsInLastHour(now) < s.cfg.MaxSessionsPerHour {
		prompt, err := ComposeMetaReview(ctx, s.db, s.ws, s.cfg.PlayerID, now)
		if err != nil {
			return false, err
		}
		s.sessionStarts = append(s.sessionStarts, now)
		fmt.Println("captain: starting meta-review session")
		if err := s.runner.Run(ctx, prompt); err != nil {
			return true, err // marker not written -> retried next day-window tick
		}
		if err := MarkMetaReviewDone(s.ws, now); err != nil {
			return true, err
		}
		ran = true
	}
	if s.fixer != nil {
		acted, err := s.fixer.ProcessOne(ctx, now)
		if err != nil {
			fmt.Printf("captain fixer: %v\n", err)
		}
		ran = ran || acted
	}
	return ran, nil
}
```

- [ ] **Step 4: Wire into cmd/captain**

In `gobot/cmd/captain/main.go`, after `sup := captainsup.NewSupervisor(...)`:

```go
	fixerFactory := func(workDir string) captainsup.SessionRunner {
		return captainsup.NewClaudeRunner(
			cfg.Captain.ClaudeBin, cfg.Captain.Model, workDir,
			time.Duration(cfg.Captain.FixSessionTimeoutMinutes)*time.Minute,
		)
	}
	sup.SetFixer(captainsup.NewFixer(ws, fixerFactory, cfg.Captain))
```

- [ ] **Step 5: Run the full captain test suite**

Run: `cd gobot && go test ./internal/captain/ -v && go build ./...`
Expected: ALL tests pass (plan 1 + plan 2), build clean.

- [ ] **Step 6: Commit**

```bash
git add gobot/internal/captain/supervisor.go gobot/internal/captain/supervisor_test.go gobot/cmd/captain/main.go
git commit -m "feat(captain): wire fix pipeline and daily meta-review into the supervisor"
```

---

### Task 7: End-to-end pipeline validation (propose-only, then auto-merge)

**Files:** none created — this is a validation task with evidence committed at the end.

- [ ] **Step 1: Seed a real bug report and run propose-only**

With daemon + captain configured (plan 1 Task 10) and `captain.auto_merge: false`: write a small, real, known-fixable report (pick an actual TODO from the codebase, e.g. the `isShipStuck` stub in `internal/domain/daemon/health_monitor.go` which always returns false) to `captain/reports/bugs/2026-07-02-ship-stuck-stub.md` with `status: new`, `kind: fix` frontmatter.

Run: `cd gobot && ./bin/captain --once`
Expected: fixer creates `captain/fix-2026-07-02-ship-stuck-stub` branch, runs a fix session, gate runs. On gate pass: report becomes `status: awaiting_human`, branch exists for your review (`git -C gobot branch --list 'captain/*'`).

- [ ] **Step 2: Review the branch like a hostile reviewer**

Run: `cd gobot && git diff main..captain/fix-2026-07-02-ship-stuck-stub`
Check: failing-test-first commit order, minimal diff, no migrations touched, no files outside gobot. If quality is poor, tighten `FixPrompt` wording and re-run — do NOT enable auto-merge until a few propose-only fixes look mergeable as-is.

- [ ] **Step 3: Flip auto-merge and verify the full loop once**

Set `captain.auto_merge: true`, seed one more small report, run `./bin/captain --once`.
Expected: gate pass → squash-merge onto main → `make restart-daemon` runs → report `status: merged`. Verify: `git -C gobot log -1 --stat` shows the captain commit; `./bin/spacetraders health` shows the daemon back up.

- [ ] **Step 4: Commit the evidence and update the spec status**

```bash
git add captain/ gobot/
git commit -m "test(captain): fix pipeline validated end-to-end (propose-only + auto-merge)"
```

Then edit `docs/superpowers/specs/2026-07-02-autonomous-captain-design.md` header `**Status:** Draft — awaiting user review` → `**Status:** Implemented (phases 1-4)` and commit:

```bash
git add docs/superpowers/specs/2026-07-02-autonomous-captain-design.md
git commit -m "docs: mark autonomous captain spec implemented"
```
