package watchkeeper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProvisionWorktreeWiresBeadsRedirect(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)

	// The beads database lives in the main checkout's .beads/. config.yaml is
	// tracked; the redirect + Dolt db dir are gitignored runtime state.
	mainBeads := filepath.Join(repo, ".beads")
	require.NoError(t, os.MkdirAll(mainBeads, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mainBeads, "config.yaml"), []byte("issue-prefix: sp\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(mainBeads, ".gitignore"), []byte("redirect\nembeddeddolt/\n"), 0o644))
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add beads")

	wt, err := CreateWorktree(repo, "captain/fix-beads")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	require.NoError(t, ProvisionWorktree(wt.Dir))

	// A worktree checks out the tracked .beads/ files but not the gitignored
	// Dolt db, so bd must be redirected to the main checkout to find real beads.
	redirect := filepath.Join(wt.Dir, ".beads", "redirect")
	require.FileExists(t, redirect)
	raw, err := os.ReadFile(redirect)
	require.NoError(t, err)
	target := strings.TrimSpace(string(raw))
	require.True(t, filepath.IsAbs(target), "redirect target must be absolute: %q", target)
	// Proves the redirect points at the real main .beads (symlink-form agnostic).
	require.FileExists(t, filepath.Join(target, "config.yaml"))

	// bd's recommended permissions (0700); also silences its per-call warning.
	info, err := os.Stat(filepath.Join(wt.Dir, ".beads"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestProvisionWorktreeSkipsBeadsWhenAbsent(t *testing.T) {
	t.Parallel()
	// A repo that does not use beads must provision without error and without
	// fabricating a .beads/ dir in the worktree.
	repo := initScratchRepo(t)
	wt, err := CreateWorktree(repo, "captain/fix-nobeads")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	require.NoError(t, ProvisionWorktree(wt.Dir))
	require.NoDirExists(t, filepath.Join(wt.Dir, ".beads"))
}

// TestProvisionWorktreePreservesRegeneratedProto is the sp-a3r9 regression:
// ProvisionWorktree's job is to supply gitignored build artifacts a worktree
// LACKS, never to revert a tracked file the branch intentionally changed. A
// worktree that regenerated pkg/proto/daemon (e.g. a new daemon RPC field)
// has a copy that differs from repoRoot's stale one; provision must leave it
// alone instead of clobbering it with repoRoot's copy.
func TestProvisionWorktreePreservesRegeneratedProto(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)

	protoDir := filepath.Join(repo, "gobot", "pkg", "proto", "daemon")
	require.NoError(t, os.MkdirAll(protoDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(protoDir, "daemon.pb.go"),
		[]byte("package daemon\n// stale main version\n"), 0o644))
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add proto")

	wt, err := CreateWorktree(repo, "captain/fix-proto-regen")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	// Simulate the fix branch regenerating proto in the worktree, ahead of
	// repoRoot's stale committed copy.
	wtProtoFile := filepath.Join(wt.Dir, "gobot", "pkg", "proto", "daemon", "daemon.pb.go")
	regenerated := []byte("package daemon\n// regenerated: adds GetInputsOnly\n")
	require.NoError(t, os.WriteFile(wtProtoFile, regenerated, 0o644))

	require.NoError(t, ProvisionWorktree(wt.Dir))

	got, err := os.ReadFile(wtProtoFile)
	require.NoError(t, err)
	require.Equal(t, regenerated, got,
		"provision must never revert a worktree's regenerated proto to repoRoot's stale copy")
}

// TestProvisionWorktreeCopiesMissingProto preserves provision's original
// purpose: a genuinely-gitignored artifact the worktree checkout never
// received must still be supplied from repoRoot.
func TestProvisionWorktreeCopiesMissingProto(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)

	protoDir := filepath.Join(repo, "gobot", "pkg", "proto", "daemon")
	require.NoError(t, os.MkdirAll(protoDir, 0o755))
	content := []byte("package daemon\n// main version\n")
	require.NoError(t, os.WriteFile(filepath.Join(protoDir, "daemon.pb.go"), content, 0o644))
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add proto")

	wt, err := CreateWorktree(repo, "captain/fix-proto-missing")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	wtProtoFile := filepath.Join(wt.Dir, "gobot", "pkg", "proto", "daemon", "daemon.pb.go")
	require.NoError(t, os.Remove(wtProtoFile))

	require.NoError(t, ProvisionWorktree(wt.Dir))

	got, err := os.ReadFile(wtProtoFile)
	require.NoError(t, err)
	require.Equal(t, content, got, "provision must supply a genuinely missing artifact from repoRoot")
}

// TestProvisionWorktreeRewritesIdenticalProto guards against an overly-broad
// fix that skips copying whenever the destination merely exists (which would
// also happen to pass the "differs" regression test above for the wrong
// reason). A destination that is byte-identical to repoRoot's copy must
// still be (re)written, matching the "missing-or-identical" contract — proven
// here by an mtime bump, since content equality alone can't distinguish a
// real copy from a no-op skip.
func TestProvisionWorktreeRewritesIdenticalProto(t *testing.T) {
	t.Parallel()
	repo := initScratchRepo(t)

	protoDir := filepath.Join(repo, "gobot", "pkg", "proto", "daemon")
	require.NoError(t, os.MkdirAll(protoDir, 0o755))
	content := []byte("package daemon\n// main version\n")
	require.NoError(t, os.WriteFile(filepath.Join(protoDir, "daemon.pb.go"), content, 0o644))
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "add proto")

	wt, err := CreateWorktree(repo, "captain/fix-proto-identical")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	wtProtoFile := filepath.Join(wt.Dir, "gobot", "pkg", "proto", "daemon", "daemon.pb.go")
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(wtProtoFile, old, old))

	require.NoError(t, ProvisionWorktree(wt.Dir))

	got, err := os.ReadFile(wtProtoFile)
	require.NoError(t, err)
	require.Equal(t, content, got)

	info, err := os.Stat(wtProtoFile)
	require.NoError(t, err)
	require.True(t, info.ModTime().After(old),
		"a destination identical to repoRoot's copy must still be written, not skipped outright")
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
