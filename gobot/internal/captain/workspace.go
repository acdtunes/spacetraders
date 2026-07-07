// Package watchkeeper implements the watchkeeper supervisor: the deterministic
// plumbing that turns outbox events + heartbeats into captain wake signals.
package watchkeeper

import (
	"os"
	"path/filepath"
)

type Workspace struct {
	dir string
}

func NewWorkspace(dir string) Workspace { return Workspace{dir: dir} }

func (w Workspace) Dir() string { return w.dir }

// Disabled reports the master kill switch (a plain file, flippable over SSH).
func (w Workspace) Disabled() bool {
	_, err := os.Stat(filepath.Join(w.dir, "DISABLED"))
	return err == nil
}

func (w Workspace) DisabledFixes() bool {
	_, err := os.Stat(filepath.Join(w.dir, "DISABLED_FIXES"))
	return err == nil
}

// TouchDisabled writes the kill switch iff it does not yet exist and reports
// whether this call created it. An existing switch is never rewritten, and no
// method here ever removes it: clearing DISABLED is the Admiral's act alone.
func (w Workspace) TouchDisabled(reason string) (bool, error) {
	path := filepath.Join(w.dir, "DISABLED")
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(reason+"\n"), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
