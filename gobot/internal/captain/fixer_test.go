package watchkeeper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProvisionWorktreeWiresBeadsRedirect(t *testing.T) {
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
	// A repo that does not use beads must provision without error and without
	// fabricating a .beads/ dir in the worktree.
	repo := initScratchRepo(t)
	wt, err := CreateWorktree(repo, "captain/fix-nobeads")
	require.NoError(t, err)
	defer func() { _ = wt.Remove(repo) }()

	require.NoError(t, ProvisionWorktree(wt.Dir))
	require.NoDirExists(t, filepath.Join(wt.Dir, ".beads"))
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
