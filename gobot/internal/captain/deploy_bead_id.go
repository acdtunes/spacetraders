package watchkeeper

import (
	"os/exec"
	"regexp"
	"strings"
)

// beadIDPattern matches a trailing "(sp-xxxx)"-shaped bead-id token at the end
// of a commit subject line, e.g. "...self-diagnosing abort (sp-2sam)" ->
// "sp-2sam". Generic across any "<prefix>-<suffix>" token (not hardcoded to
// "sp-") so it keeps working if the bead prefix convention ever shifts.
// Trailing whitespace after the closing paren is tolerated.
var beadIDPattern = regexp.MustCompile(`\(([a-zA-Z][a-zA-Z0-9]*-[a-zA-Z0-9]+)\)\s*$`)

// parseBeadID extracts a trailing bead-id token from subject, or "" if none
// is present. Pure string function, no I/O — see BeadIDFromHEAD for the git
// call this feeds.
func parseBeadID(subject string) string {
	m := beadIDPattern.FindStringSubmatch(strings.TrimRight(subject, "\r\n \t"))
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

// BeadIDFromHEAD returns a best-effort bead-id lookup, run against the git
// repo containing dir (git itself resolves dir upward to the enclosing
// worktree, so dir need not be the repo root). It is injected into
// RecordDeployIfChanged specifically so that function is testable without
// invoking real git.
//
// This is the one runtime git-shell call in this bead, deliberately kept
// separate from the SquashMerge/BranchContainsMain machinery in worktree.go
// (off-limits per spec). It NEVER panics, blocks, or returns an error: git
// being unavailable, dir not being inside a repo, or HEAD's subject carrying
// no bead-id token all degrade to "", identical in effect to a build with no
// bead-id available — bead id is garnish on top of the commit (the actual
// deploy signal) and must never gate the emit.
func BeadIDFromHEAD(dir string) func() string {
	return func() string {
		out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%s").Output()
		if err != nil {
			return ""
		}
		return parseBeadID(string(out))
	}
}
