package captainsup

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type MigrationReport struct {
	Strategy  int
	Decisions int
	Lessons   int
	Backlog   int
	Bugs      int
	Commands  [][]string
}

type bdRun func(args ...string) (string, error)

var lessonDelimiter = regexp.MustCompile(`^L\d+\b`)

func Migrate(ctx context.Context, b *BeadsClient, stateDir, reportsDir string, apply bool) (MigrationReport, error) {
	var rep MigrationReport

	run := func(args ...string) (string, error) {
		rep.Commands = append(rep.Commands, append([]string{b.BDBin}, args...))
		if apply {
			return b.Exec(ctx, b.BDBin, args...)
		}
		return "<new-id>", nil
	}

	strategyPath := filepath.Join(stateDir, "strategy.md")
	if fileExists(strategyPath) {
		if _, err := run("create", "Fleet strategy", "-t", "design", "-l", "strategy", "--body-file", strategyPath); err != nil {
			return rep, err
		}
		rep.Strategy++
	}

	if err := migrateDecisions(filepath.Join(stateDir, "decisions.jsonl"), run, &rep); err != nil {
		return rep, err
	}

	for _, name := range []string{"lessons.md", "lessons-archive.md"} {
		if err := migrateLessons(filepath.Join(stateDir, name), run, &rep); err != nil {
			return rep, err
		}
	}

	if err := migrateBullets(filepath.Join(stateDir, "friction.md"), `^- `, "friction", run, &rep); err != nil {
		return rep, err
	}
	if err := migrateBullets(filepath.Join(stateDir, "improvement-backlog.md"), `^## `, "backlog", run, &rep); err != nil {
		return rep, err
	}

	if err := migrateBugReports(reportsDir, run, &rep); err != nil {
		return rep, err
	}

	return rep, nil
}

func migrateDecisions(path string, run bdRun, rep *MigrationReport) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var decision struct {
			ID       string `json:"id"`
			Action   string `json:"action"`
			Decision string `json:"decision"`
			Text     string `json:"text"`
			Lesson   string `json:"lesson"`
			Verdict  string `json:"verdict"`
			Outcome  string `json:"outcome"`
		}
		if err := json.Unmarshal([]byte(line), &decision); err != nil {
			return err
		}
		title := truncateRunes(firstNonEmpty(decision.Action, decision.Decision, decision.Text, decision.Lesson, decision.Verdict, "decision "+decision.ID), 80)
		id, err := run("create", title, "-t", "decision", "-l", "migrated", "--silent")
		if err != nil {
			return err
		}
		id = strings.TrimSpace(id)
		if strings.TrimSpace(decision.Outcome) != "" {
			if _, err := run("note", id, "outcome: "+decision.Outcome); err != nil {
				return err
			}
			if _, err := run("close", id, "--reason", "historical"); err != nil {
				return err
			}
		}
		rep.Decisions++
	}
	return scanner.Err()
}

func migrateLessons(path string, run bdRun, rep *MigrationReport) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var current []string
	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		lesson := strings.TrimSpace(strings.Join(current, " "))
		current = nil
		if lesson == "" {
			return nil
		}
		if _, err := run("remember", lesson); err != nil {
			return err
		}
		rep.Lessons++
		return nil
	}

	for _, line := range strings.Split(string(data), "\n") {
		if lessonDelimiter.MatchString(line) {
			if err := flush(); err != nil {
				return err
			}
			current = []string{strings.TrimSpace(line)}
			continue
		}
		if len(current) > 0 && strings.TrimSpace(line) != "" {
			current = append(current, strings.TrimSpace(line))
		}
	}
	return flush()
}

func migrateBullets(path, pattern, label string, run bdRun, rep *MigrationReport) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	prefix := regexp.MustCompile(pattern)
	for _, line := range strings.Split(string(data), "\n") {
		if !prefix.MatchString(line) {
			continue
		}
		title := strings.TrimSpace(prefix.ReplaceAllString(line, ""))
		if title == "" {
			continue
		}
		args := []string{"create", truncateRunes(title, 120), "-t", "feature", "-l", label, "-p", "3"}
		if len([]rune(title)) > 120 {
			args = append(args, "-d", title)
		}
		if _, err := run(args...); err != nil {
			return err
		}
		rep.Backlog++
	}
	return nil
}

func migrateBugReports(dir string, run bdRun, rep *MigrationReport) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(dir, name)
		title, status, err := readReportFrontmatter(path)
		if err != nil {
			return err
		}
		if !isNonTerminalStatus(status) {
			continue
		}
		if title == "" {
			title = strings.TrimSuffix(name, ".md")
		}
		if _, err := run("create", truncateRunes(title, 400), "-t", "bug", "-l", "shipwright", "--body-file", path); err != nil {
			return err
		}
		rep.Bugs++
	}
	return nil
}

func readReportFrontmatter(path string) (title, status string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	inFrontmatter := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			break
		}
		if !inFrontmatter {
			if trimmed == "" {
				continue
			}
			break
		}
		if strings.HasPrefix(trimmed, "title:") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "title:"))
		}
		if strings.HasPrefix(trimmed, "status:") {
			status = strings.TrimSpace(strings.TrimPrefix(trimmed, "status:"))
		}
	}
	return title, status, nil
}

func isNonTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "new", "in_progress", "gate_failed", "awaiting_human":
		return true
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func truncateRunes(s string, limit int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
