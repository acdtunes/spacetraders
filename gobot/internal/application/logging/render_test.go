package logging

import (
	"math"
	"strings"
	"testing"
	"time"
)

var stamp = time.Date(2026, 7, 30, 23, 13, 2, 0, time.UTC)

// The defect, stated as a test: fields handed to the sink must reach the text.
// Before this renderer existed the stdout sink printed the message and dropped
// the payload, so a grep of daemon.log for ANY field name — on any container,
// for any field — returned zero lines.
func TestFormatLine_CarriesTheStructuredFields(t *testing.T) {
	line := FormatLine(stamp, "probe_sensing_coordinator-player-5", "INFO", "Parked sensing cycle: bought 6",
		map[string]interface{}{"yards_need_presence": 64, "yard_slots_queued": 71})

	for _, want := range []string{`"yards_need_presence":64`, `"yard_slots_queued":71`} {
		if !strings.Contains(line, want) {
			t.Errorf("the rendered line does not carry %s: %q", want, line)
		}
	}
}

// The message keeps its leading position and its shape, so every standing grep
// against daemon.log still matches. The fields are a suffix, never a rewrite.
func TestFormatLine_PrefixIsUnchangedByTheFields(t *testing.T) {
	const prefix = "[2026-07-30T23:13:02Z] [c-1] INFO: bought 6"

	if got := FormatLine(stamp, "c-1", "INFO", "bought 6", nil); got != prefix {
		t.Fatalf("a payload-less entry changed shape:\nwant %q\ngot  %q", prefix, got)
	}
	if got := FormatLine(stamp, "c-1", "INFO", "bought 6", map[string]interface{}{"a": 1}); !strings.HasPrefix(got, prefix+" ") {
		t.Fatalf("the fields did not append as a suffix: %q", got)
	}
}

// An empty payload renders nothing rather than an empty object, so the millions
// of payload-less lines the daemon already writes are byte-identical.
func TestFormatFields_EmptyPayloadRendersNothing(t *testing.T) {
	for name, payload := range map[string]map[string]interface{}{
		"nil":   nil,
		"empty": {},
	} {
		if got := FormatFields(payload); got != "" {
			t.Errorf("%s payload rendered %q, want empty", name, got)
		}
	}
}

// THE FALLBACK IS THE POINT. json.Marshal fails on NaN and ±Inf, and these
// payloads carry computed rates and brake factors — values produced by division.
// Returning empty there would mean the one tick whose numbers went strange is
// the one tick with no fields at all, which is the failure this file exists to
// end. The field NAMES an operator greps for must survive a value that will not
// marshal.
func TestFormatFields_UnmarshalableValueStillRendersTheFieldNames(t *testing.T) {
	rendered := FormatFields(map[string]interface{}{
		"action":     "parked_sensing_cycle",
		"pacer_rate": math.NaN(),
	})

	if rendered == "" {
		t.Fatal("a payload with a NaN rendered EMPTY — the tick whose numbers went strange " +
			"is exactly the tick an operator needs the fields for")
	}
	for _, want := range []string{"action", "parked_sensing_cycle", "pacer_rate"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the fallback dropped %q: %s", want, rendered)
		}
	}
}

// Key order is stable across renderings, so a diff of two daemon.log lines shows
// what changed rather than a reshuffle. json.Marshal sorts map keys; so does the
// fmt fallback.
func TestFormatFields_KeyOrderIsStable(t *testing.T) {
	payload := map[string]interface{}{"zulu": 1, "alpha": 2, "mike": 3}

	first := FormatFields(payload)
	for i := 0; i < 20; i++ {
		if got := FormatFields(payload); got != first {
			t.Fatalf("rendering is not stable:\nfirst %s\nthen  %s", first, got)
		}
	}
	if !strings.HasPrefix(first, `{"alpha"`) {
		t.Errorf("keys are not sorted: %s", first)
	}
}
