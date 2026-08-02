package grpc

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
)

// LogEntry represents a single log message from a container
type LogEntry struct {
	Timestamp time.Time
	Level     string
	Message   string
	Metadata  map[string]interface{}
}

// Log adds a log entry (implements common.ContainerLogger interface)
func (r *ContainerRunner) Log(level, message string, metadata map[string]interface{}) {
	r.mu.Lock()
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
		Metadata:  metadata,
	}

	r.logs = append(r.logs, entry)
	r.mu.Unlock()

	// Print to stdout, MESSAGE AND FIELDS BOTH.
	//
	// logging.FormatLine owns the WHOLE rendering, message and metadata together, so
	// a field added at any call site reaches daemon.log without a change here.
	// Rendering the message alone strands every structured field in
	// container_logs.metadata, invisible to a grep of daemon.log.
	fmt.Fprintln(r.stdout(), logging.FormatLine(
		entry.Timestamp,
		r.containerEntity.ID(),
		level,
		message,
		metadata,
	))

	// Persist to database (async to avoid blocking)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dbOperationTimeout)
		defer cancel()

		if err := r.logRepo.Log(ctx, r.containerEntity.ID(), r.containerEntity.PlayerID(), message, level, metadata); err != nil {
			// Log error to stdout if DB write fails (but don't block execution)
			fmt.Printf("[%s] [%s] ERROR: Failed to persist log to DB: %v\n",
				time.Now().Format(time.RFC3339),
				r.containerEntity.ID(),
				err,
			)
		}
	}()
}

// stdout is the sink Log renders into. Nil means os.Stdout, which is every
// production path — the field exists so a test can read back the bytes this
// runner actually emits rather than assert against a re-implementation of the
// format, which is the assertion that let the dropped payload survive.
func (r *ContainerRunner) stdout() io.Writer {
	if r.out != nil {
		return r.out
	}
	return os.Stdout
}

// log is a lowercase alias for backward compatibility with existing code
func (r *ContainerRunner) log(level, message string, metadata map[string]interface{}) {
	r.Log(level, message, metadata)
}

// GetLogs returns all logs for this container
func (r *ContainerRunner) GetLogs(limit *int, level *string) []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	filtered := make([]LogEntry, 0)
	for _, log := range r.logs {
		if level != nil && log.Level != *level {
			continue
		}
		filtered = append(filtered, log)
	}

	if limit != nil && *limit < len(filtered) {
		start := len(filtered) - *limit
		return filtered[start:]
	}

	return filtered
}
