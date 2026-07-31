package logging

// render.go is the one place a container log entry becomes text.
//
// IT EXISTS BECAUSE THE STRUCTURED HALF USED TO GO NOWHERE. Every call site in
// the daemon passes two things to Log — a human-readable message and a
// map[string]interface{} of fields — and the stdout sink printed only the first.
// The map was persisted to container_logs.metadata and was therefore recoverable
// by hand, but daemon.log, which is where an operator actually looks, showed no
// trace of it: a grep for any field name on this box returned zero lines, for
// every field, on every container. Counters built specifically so that an engine
// losing every decision would not look identical to an idle one were unreadable
// on the channel they were built for (sp-qkskz).
//
// So the fix is not per-field. A field added tomorrow renders here for free, and
// no future author has to know that the sink had a hole in it.
//
// Rendered as a JSON suffix on the same line, never as a second line: the entries
// interleave across containers and a payload on its own line could not be
// attributed to the message it belongs to.

import (
	"encoding/json"
	"fmt"
	"time"
)

// FormatLine renders one container log entry exactly as the daemon prints it.
//
// The message keeps its leading position and its original shape, so every
// standing grep against daemon.log still matches — the fields are a SUFFIX, and
// an entry with no fields is byte-identical to what this sink printed before.
func FormatLine(ts time.Time, containerID, level, message string, metadata map[string]interface{}) string {
	line := fmt.Sprintf("[%s] [%s] %s: %s", ts.Format(time.RFC3339), containerID, level, message)
	if fields := FormatFields(metadata); fields != "" {
		line += " " + fields
	}
	return line
}

// FormatFields renders a log payload as compact JSON, and NEVER returns empty
// for a non-empty payload.
//
// The fallback is the point rather than defensive noise. json.Marshal fails on
// values that reach these payloads in practice — a NaN or ±Inf float is the live
// one, since rates and brake factors are computed by division — and on that
// failure the DB sink stores NULL, silently. If this returned empty there too,
// the one tick whose numbers went strange would be the one tick with no fields
// at all, which is precisely the failure mode this file was written to end. Go's
// map formatting cannot fail and prints keys in sorted order, so the field names
// an operator greps for survive even when the values will not marshal.
func FormatFields(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	if encoded, err := json.Marshal(metadata); err == nil {
		return string(encoded)
	}
	return fmt.Sprintf("%v", metadata)
}
