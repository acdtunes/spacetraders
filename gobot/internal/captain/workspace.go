// Package captainsup implements the captain supervisor: the deterministic
// plumbing that turns outbox events + heartbeats into claude -p sessions.
package captainsup

import (
	"strings"
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

// InboxPath is the Admiral->Captain message channel (workspace root, not
// state/: the captain owns state/, the Admiral owns the inbox). The
// supervisor injects its content into the next session and clears it after
// the session succeeds.
func (w Workspace) InboxPath() string {
	return filepath.Join(w.dir, "inbox.md")
}

// TrimLog bounds state/<name> to roughly maxBytes, archiving the overflow to
// state/<name-without-ext>.archive<ext>. The cut lands on an entry boundary
// ("\n## ") so no entry is split; the file's first line (title header) is
// preserved. Keeps prompt and full-file-read costs bounded as the captain's
// history grows.
func (w Workspace) TrimLog(name string, maxBytes int) error {
	path := w.StatePath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) <= maxBytes {
		return nil
	}

	content := string(data)
	headerEnd := strings.Index(content, "\n## ")
	if headerEnd < 0 {
		return nil // no entry structure; refuse to cut blindly
	}
	header := content[:headerEnd]

	// Find the first entry boundary at or after the overflow cutoff.
	cutoff := len(content) - maxBytes
	cut := strings.Index(content[cutoff:], "\n## ")
	if cut < 0 {
		return nil // a single giant entry; nothing safe to trim
	}
	cut += cutoff
	archived := content[headerEnd:cut]
	kept := content[cut:]

	ext := filepath.Ext(name)
	archivePath := w.StatePath(strings.TrimSuffix(name, ext) + ".archive" + ext)
	af, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := af.WriteString(archived); err != nil {
		af.Close()
		return err
	}
	af.Close()

	return os.WriteFile(path, []byte(header+kept), 0o644)
}

// ReadBudgeted returns up to maxBytes of state/<name> (from the head — the
// contract puts current state at the top) and whether the file exceeds the
// budget. Oversized memory files get truncated in prompts instead of billing
// every session for unbounded context.
func (w Workspace) ReadBudgeted(name string, maxBytes int) (string, bool) {
	data, err := os.ReadFile(w.StatePath(name))
	if err != nil {
		return "", false
	}
	if len(data) <= maxBytes {
		return string(data), false
	}
	return string(data[:maxBytes]), true
}
