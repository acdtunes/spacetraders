package captainsup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var refVerbRe = regexp.MustCompile(`(?m)^## spacetraders ([a-z-]+)\s*$`)

// UnexercisedVerbs lists top-level CLI verbs present in the generated
// CLI_REFERENCE.md that never appear as invocations anywhere in the captain's
// history (live log + archive). Injected into heartbeat prompts as data: the
// captain cannot audit a capability surface it does not know it hasn't used.
func UnexercisedVerbs(ws Workspace) []string {
	ref, err := os.ReadFile(filepath.Join(ws.Dir(), "CLI_REFERENCE.md"))
	if err != nil {
		return nil
	}
	history := ws.ReadFull("captain-log.md") + ws.ReadFull("captain-log.archive.md")

	var out []string
	for _, m := range refVerbRe.FindAllStringSubmatch(string(ref), -1) {
		verb := m[1]
		if verb == "help" || verb == "config" || verb == "player" {
			continue // administrative, not fleet capabilities
		}
		if !strings.Contains(history, "spacetraders "+verb) {
			out = append(out, verb)
		}
	}
	return out
}
