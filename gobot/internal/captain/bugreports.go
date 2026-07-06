package captainsup

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	statusNew           = "new"
	statusInProgress    = "in_progress"
	statusGateFailed    = "gate_failed"
	statusAwaitingHuman = "awaiting_human"
	statusMerged        = "merged"

	kindFix        = "fix"
	kindFeature    = "feature"
	kindAutomation = "automation"
)

type BugReport struct {
	Path   string
	Slug   string
	Title  string
	Status string // new | in_progress | merged | gate_failed | awaiting_human
	Kind   string // fix | feature
}

// ScanReports reads every .md file in dir and parses its frontmatter.
// Files without frontmatter are treated as {status: new, kind: fix} so a
// hastily written report still enters the pipeline.
func ScanReports(dir string) ([]BugReport, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []BugReport
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		r := BugReport{
			Path:   path,
			Slug:   strings.TrimSuffix(e.Name(), ".md"),
			Status: statusNew,
			Kind:   kindFix,
		}
		parseFrontmatter(string(data), &r)
		out = append(out, r)
	}
	return out, nil
}

func parseFrontmatter(content string, r *BugReport) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			return
		}
		key, val, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "status":
			r.Status = val
		case "kind":
			r.Kind = val
		case "title":
			r.Title = val
		}
	}
}

// SetReportStatus rewrites (or inserts) the status field, preserving the body.
func SetReportStatus(path, status string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		replaced := false
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				if !replaced {
					lines = append(lines[:i], append([]string{"status: " + status}, lines[i:]...)...)
				}
				break
			}
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "status:") {
				lines[i] = "status: " + status
				replaced = true
			}
		}
		content = strings.Join(lines, "\n")
	} else {
		content = "---\nstatus: " + status + "\nkind: fix\n---\n" + content
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
