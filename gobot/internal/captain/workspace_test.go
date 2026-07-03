package captainsup

import (
	"strings"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDisabledKillSwitch(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspace(dir)
	require.False(t, ws.Disabled())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "DISABLED"), nil, 0o644))
	require.True(t, ws.Disabled())
}

func TestTailReturnsLastBytesAndEmptyWhenMissing(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	require.Equal(t, "", ws.Tail("captain-log.md", 100))

	content := "OLD-OLD-OLD\nNEW-TAIL"
	require.NoError(t, os.WriteFile(ws.StatePath("captain-log.md"), []byte(content), 0o644))
	require.Equal(t, "NEW-TAIL", ws.Tail("captain-log.md", 8))
	require.Equal(t, content, ws.Tail("captain-log.md", 10000))
}

func TestTrimLogArchivesOldEntriesAtBoundary(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)

	var b []byte
	b = append(b, []byte("# Captain's log\n\n")...)
	for i := 1; i <= 40; i++ {
		b = append(b, []byte(fmt.Sprintf("## session %d\n%s\n", i, strings.Repeat("x", 400)))...)
	}
	require.NoError(t, os.WriteFile(ws.StatePath("captain-log.md"), b, 0o644))

	require.NoError(t, ws.TrimLog("captain-log.md", 4096))

	kept, _ := os.ReadFile(ws.StatePath("captain-log.md"))
	require.LessOrEqual(t, len(kept), 4096+512, "trimmed near target")
	require.True(t, strings.HasPrefix(string(kept), "# Captain's log"), "header preserved")
	require.Contains(t, string(kept), "## session 40", "newest entries kept")
	require.NotContains(t, string(kept), "## session 1\n", "oldest entries archived")

	arch, err := os.ReadFile(ws.StatePath("captain-log.archive.md"))
	require.NoError(t, err)
	require.Contains(t, string(arch), "## session 1")

	require.NoError(t, ws.TrimLog("captain-log.md", 1<<20))
}
