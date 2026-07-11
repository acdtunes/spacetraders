package commands

import "sync"

// capturedLogEntry records one logged line for assertions.
type capturedLogEntry struct {
	level   string
	message string
}

// capturingLogger records logged entries so tests can assert what reaches the
// container log stream. The renderer prints only level+message and DROPS the
// metadata map (container_runner.go), so a cause hidden in metadata never reaches
// an operator - the entire point of these regressions.
//
// Coordinators fan out concurrent goroutines sharing one ContainerLogger pulled
// from context, so Log is called concurrently. Every real implementation
// (ContainerRunner.Log) guards its buffer with a mutex; this test double must
// honor the same contract or -race fires (sp-8t30). Callers read entries via
// snapshot(), never the field directly.
//
// (sp-jav2 X2: this helper previously lived in run_manufacturing_task_worker_test.go,
// which was deleted with the retired parallel coordinator. It is preserved here for
// the surviving goods_factory_coordinator tests that assert verbatim log surfacing.)
type capturingLogger struct {
	mu      sync.Mutex
	entries []capturedLogEntry
}

func (l *capturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, capturedLogEntry{level: level, message: message})
}

// snapshot returns a copy of the recorded entries under lock. Reads must go
// through this rather than touching entries directly: background goroutines can
// still append a line while a test reads the assertions.
func (l *capturingLogger) snapshot() []capturedLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]capturedLogEntry, len(l.entries))
	copy(out, l.entries)
	return out
}
