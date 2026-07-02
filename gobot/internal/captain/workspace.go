// Package captainsup implements the captain supervisor: the deterministic
// plumbing that turns outbox events + heartbeats into claude -p sessions.
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

func (w Workspace) StatePath(name string) string {
	return filepath.Join(w.dir, "state", name)
}

// Tail returns up to maxBytes from the end of state/<name>; "" if missing.
func (w Workspace) Tail(name string, maxBytes int) string {
	data, err := os.ReadFile(w.StatePath(name))
	if err != nil {
		return ""
	}
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}
	return string(data)
}

// ReadFull returns the whole state/<name>; "" if missing.
func (w Workspace) ReadFull(name string) string {
	data, err := os.ReadFile(w.StatePath(name))
	if err != nil {
		return ""
	}
	return string(data)
}
