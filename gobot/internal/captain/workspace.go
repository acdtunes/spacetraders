// Package captainsup implements the captain supervisor: the deterministic
// plumbing that turns outbox events + heartbeats into captain wake signals.
package captainsup

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
