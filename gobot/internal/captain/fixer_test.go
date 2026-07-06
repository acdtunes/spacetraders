package captainsup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGateDirResolvesMonorepoModule(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "gobot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "gobot", "go.mod"), []byte("module x\n"), 0o644))
	require.Equal(t, filepath.Join(root, "gobot"), gateDir(root), "monorepo: gate runs in the module subdir")

	flat := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(flat, "go.mod"), []byte("module y\n"), 0o644))
	require.Equal(t, flat, gateDir(flat), "flat repo: gate runs at the root")
}
