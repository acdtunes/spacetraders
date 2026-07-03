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
		MaxFixesPerDay: 3, MaxFeaturesPerDay: 2, FixSessionTimeoutMinutes: 1, MaxFeatureDiffLines: 400,
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

func TestGateDirResolvesMonorepoModule(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "gobot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "gobot", "go.mod"), []byte("module x\n"), 0o644))
	require.Equal(t, filepath.Join(root, "gobot"), gateDir(root), "monorepo: gate runs in the module subdir")

	flat := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(flat, "go.mod"), []byte("module y\n"), 0o644))
	require.Equal(t, flat, gateDir(flat), "flat repo: gate runs at the root")
}

const sampleAutomation = `---
title: Arbitrage route coordinator
status: new
kind: automation
---

## Design
Coordinator that discovers idle haulers and runs buy-low/sell-high routes.
`

func TestAutomationKindAutoMerges(t *testing.T) {
	stub := &fixStubRunner{write: "package main\n\n// Auto.\nfunc Auto() bool { return true }\n"}
	fixer, reportPath, repo := newFixerFixture(t, stub, true) // auto_merge ON
	require.NoError(t, os.WriteFile(reportPath, []byte(sampleAutomation), 0o644))

	acted, err := fixer.ProcessOne(context.Background(), time.Now())
	require.NoError(t, err)
	require.True(t, acted)

	reports, _ := ScanReports(filepath.Dir(reportPath))
	require.Equal(t, "merged", reports[0].Status,
		"automations are full citizens: gate-passed means merged")
	require.FileExists(t, filepath.Join(repo, "fix.go"))
}
