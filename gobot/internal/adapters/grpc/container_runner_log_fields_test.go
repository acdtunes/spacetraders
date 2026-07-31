package grpc

// The sink itself, read back byte for byte.
//
// probe_sensing_yard_observability_test.go proves the coordinator's numbers
// survive the production renderer. This file proves the runner actually USES
// that renderer — the seam a change could quietly cut, leaving the renderer
// correct, its own tests green, and daemon.log blind again exactly as it was
// before sp-qkskz.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// silentLogRepo stands in for the Postgres sink, which this file is not about.
// It must not be nil: Log persists asynchronously, and a nil repo would panic on
// a goroutine and take the test binary with it rather than fail a case.
type silentLogRepo struct{}

func (silentLogRepo) Log(context.Context, string, int, string, string, map[string]interface{}) error {
	return nil
}

func (silentLogRepo) GetLogs(context.Context, string, int, int, *string, *time.Time) ([]persistence.ContainerLogEntry, error) {
	return nil, nil
}

func (silentLogRepo) GetLogsWithOffset(context.Context, string, int, int, int, *string, *time.Time) ([]persistence.ContainerLogEntry, error) {
	return nil, nil
}

// printed returns exactly what this runner wrote to its stdout sink.
func printed(t *testing.T, level, message string, metadata map[string]interface{}) string {
	t.Helper()

	var out bytes.Buffer
	r := &ContainerRunner{
		containerEntity: container.NewContainer("probe_sensing_coordinator-player-5-bb435635",
			container.ContainerType("probe_sensing_coordinator"), 5, 1, nil, nil, nil),
		logRepo: silentLogRepo{},
		out:     &out,
	}

	r.Log(level, message, metadata)

	if out.Len() == 0 {
		t.Fatal("the runner printed nothing at all, so no assertion below could " +
			"distinguish a dropped payload from a dead sink")
	}
	return out.String()
}

// THE FALSIFIER FOR THE LOG CHANNEL. A non-zero value handed to the sink is
// readable in the bytes the daemon writes to daemon.log.
//
// This fails on main: the sink printed message only, and a grep of the live
// daemon.log for any of these names returned zero lines.
func TestContainerRunnerLog_PrintsTheStructuredFields(t *testing.T) {
	line := printed(t, "INFO", "Parked sensing cycle: bought 6 reused 0", map[string]interface{}{
		"action":              "parked_sensing_cycle",
		"yards_need_presence": 64,
		"yard_slots_queued":   71,
	})

	for _, want := range []string{
		`"action":"parked_sensing_cycle"`,
		`"yards_need_presence":64`,
		`"yard_slots_queued":71`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the daemon's own log line does not carry %s\nline was:\n%s", want, line)
		}
	}
}

// The human-readable half survives intact, in front, with the container id and
// level where every standing grep expects them.
func TestContainerRunnerLog_KeepsTheMessageAndItsPrefix(t *testing.T) {
	line := printed(t, "INFO", "Parked sensing cycle: bought 6 reused 0",
		map[string]interface{}{"action": "parked_sensing_cycle"})

	for _, want := range []string{
		"[probe_sensing_coordinator-player-5-bb435635]",
		"INFO: Parked sensing cycle: bought 6 reused 0",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the line lost %q:\n%s", want, line)
		}
	}
	if message, fields := strings.Index(line, "Parked sensing"), strings.Index(line, `{"action"`); fields < message {
		t.Errorf("the fields precede the message, breaking every standing grep:\n%s", line)
	}
}

// A payload-less entry is byte-identical to what this sink printed before, so
// restoring the fields costs nothing on the lines that never had any.
func TestContainerRunnerLog_PayloadLessEntryIsUnchanged(t *testing.T) {
	line := strings.TrimRight(printed(t, "ERROR", "Container crashed (unrecoverable)", nil), "\n")

	if !strings.HasSuffix(line, "ERROR: Container crashed (unrecoverable)") {
		t.Fatalf("an entry with no fields gained a suffix: %q", line)
	}
}

// One entry is one line. The payloads interleave across containers, so a field
// block on its own line could not be attributed to the message it belongs to.
func TestContainerRunnerLog_EmitsExactlyOneLinePerEntry(t *testing.T) {
	line := printed(t, "INFO", "Parked sensing cycle", map[string]interface{}{
		"action":              "parked_sensing_cycle",
		"yards_need_presence": 64,
		"buy_refusals": []map[string]interface{}{
			{"step": "QUOTE", "yard": "X1-K39-AC2B", "reason": "no probe on offer"},
		},
	})

	if got := strings.Count(strings.TrimRight(line, "\n"), "\n"); got != 0 {
		t.Fatalf("one entry rendered across %d extra lines:\n%s", got+1, line)
	}
}
