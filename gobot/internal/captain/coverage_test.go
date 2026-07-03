package captainsup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnexercisedVerbsFromReferenceAndHistory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "state"), 0o755))
	ws := NewWorkspace(dir)
	ref := `# ref
## spacetraders ship
x
## spacetraders operations
x
## spacetraders goods
x
## spacetraders construction
x
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLI_REFERENCE.md"), []byte(ref), 0o644))
	require.NoError(t, os.WriteFile(ws.StatePath("captain-log.md"),
		[]byte("ran bin/spacetraders ship list and bin/spacetraders operations status today"), 0o644))

	verbs := UnexercisedVerbs(ws)
	require.ElementsMatch(t, []string{"goods", "construction"}, verbs,
		"verbs never seen in history are unexercised; subcommand depth ignored")
}
