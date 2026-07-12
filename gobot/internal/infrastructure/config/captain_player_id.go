package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// captainPlayerIDLine matches an indented `player_id:` assignment, capturing the
// leading label (indent + key + spacing), the numeric value, and everything after
// it (trailing spaces + any inline comment). The value is the only thing rewritten,
// so indentation and the inline comment are preserved byte-for-byte.
var captainPlayerIDLine = regexp.MustCompile(`^(\s+player_id:\s*)(\d+)(.*)$`)

// topLevelKey matches a column-0 YAML mapping key (e.g. `captain:`), which bounds
// the captain block so the repoint never touches a player_id under another block.
var topLevelKey = regexp.MustCompile(`^[A-Za-z0-9_.-]+:`)

// ResolveConfigFilePath returns the absolute path of the config.yaml that
// LoadConfig would read — honoring the SPACETRADERS_CONFIG override and the
// executable-directory fallback exactly as LoadConfig does — or "" if no config
// file can be found on disk.
//
// Writers that must edit the SAME file the daemon/watchkeeper load (e.g. the
// `universe transition` captain.player_id repoint) use this to locate it rather
// than guessing a path.
func ResolveConfigFilePath() string {
	search := resolveConfigSearch(os.Getenv(configPathEnvVar), executableDir())
	if search.file != "" {
		return search.file
	}
	for _, dir := range search.paths {
		candidate := filepath.Join(dir, "config.yaml")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// SetCaptainPlayerID rewrites the player_id value inside the top-level captain:
// block of the YAML file at path, preserving indentation, the inline comment, and
// every other byte of the file. It is a targeted line replacement, NOT a
// marshal/round-trip, so hand-maintained comments and formatting survive intact.
//
// Returns changed=false (and leaves the file untouched) when player_id is already
// the target. Returns an error when there is no captain: block or no player_id
// under it — the caller must fail loud rather than silently skip the repoint that
// otherwise wakes the supervisor as the dead prior-era player (sp-m602).
func SetCaptainPlayerID(path string, playerID int) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("failed to read config file %q: %w", path, err)
	}

	// Split/join on "\n" round-trips the file exactly, including a trailing newline.
	lines := strings.Split(string(raw), "\n")
	inCaptain := false
	target := strconv.Itoa(playerID)

	for i, line := range lines {
		if topLevelKey.MatchString(line) {
			inCaptain = strings.HasPrefix(line, "captain:")
			continue
		}
		if !inCaptain {
			continue
		}
		m := captainPlayerIDLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[2] == target {
			return false, nil // already set — byte-identical no-op
		}
		lines[i] = m[1] + target + m[3]
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644); err != nil {
			return false, fmt.Errorf("failed to write config file %q: %w", path, err)
		}
		return true, nil
	}

	return false, fmt.Errorf("no captain.player_id found in %q to repoint", path)
}
