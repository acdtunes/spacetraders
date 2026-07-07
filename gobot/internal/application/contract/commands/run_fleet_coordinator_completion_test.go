package commands

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// completionCapturingLogger records every Log call so tests can assert the
// level and message the coordinator emits for a worker-completion event.
type completionCapturingLogger struct {
	entries []capturedCompletionLog
}

type capturedCompletionLog struct {
	level   string
	message string
}

func (l *completionCapturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.entries = append(l.entries, capturedCompletionLog{level: level, message: message})
}

func (l *completionCapturingLogger) only(t *testing.T) capturedCompletionLog {
	t.Helper()
	if len(l.entries) != 1 {
		t.Fatalf("expected exactly one log entry, got %d: %+v", len(l.entries), l.entries)
	}
	return l.entries[0]
}

// A crashed worker (Success == false) must be logged at ERROR carrying
// event.Error, and must NOT count toward the completed-contracts metric. This
// pins the sp-2q2w regression: the coordinator previously logged every
// completion at INFO and incremented ContractsCompleted even for failures.
func TestRecordWorkerCompletion_FailedWorker_LogsErrorNotCounted(t *testing.T) {
	logger := &completionCapturingLogger{}
	event := navigation.WorkerCompletedEvent{
		ShipSymbol: "TORWIND-3",
		Success:    false,
		Error:      "worker crashed: navigation timeout",
	}

	counted := recordWorkerCompletion(logger, event, "Contract completed by TORWIND-3")

	if counted {
		t.Fatalf("failed worker must not count toward completed contracts")
	}
	entry := logger.only(t)
	if entry.level != "ERROR" {
		t.Fatalf("failed worker must log at ERROR, got %q", entry.level)
	}
	if !strings.Contains(entry.message, "worker crashed: navigation timeout") {
		t.Fatalf("ERROR log must carry event.Error, got %q", entry.message)
	}
	if !strings.Contains(entry.message, "TORWIND-3") {
		t.Fatalf("ERROR log must name the ship, got %q", entry.message)
	}
}

// A worker that finished successfully must be logged at INFO with the caller's
// success message and must count toward the completed-contracts metric.
func TestRecordWorkerCompletion_SuccessfulWorker_LogsInfoAndCounts(t *testing.T) {
	logger := &completionCapturingLogger{}
	event := navigation.WorkerCompletedEvent{
		ShipSymbol: "TORWIND-3",
		Success:    true,
	}

	counted := recordWorkerCompletion(logger, event, "Contract completed by TORWIND-3")

	if !counted {
		t.Fatalf("successful worker must count toward completed contracts")
	}
	entry := logger.only(t)
	if entry.level != "INFO" {
		t.Fatalf("successful worker must log at INFO, got %q", entry.level)
	}
	if entry.message != "Contract completed by TORWIND-3" {
		t.Fatalf("successful worker must log the success message verbatim, got %q", entry.message)
	}
}
