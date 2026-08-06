package apibudget

import (
	"testing"
	"time"
)

// sp-fr19d: A RATE MUST DIVIDE BY THE SPAN ACTUALLY OBSERVED.
//
// The tracker is in-memory and rebuilt on every daemon start, so for the first five minutes of a
// process its 5-minute window contains mostly time that never happened. Dividing by the full 300s
// anyway understated a saturated account by 13x, and the autosizer's api_util money guard read that
// understatement as headroom and permitted hull growth.

// saturatingEvents is `count` requests evenly spread across `span` — an account being driven at
// exactly count/span requests per second.
func saturatingEvents(start time.Time, count int, span time.Duration) []Event {
	events := make([]Event, 0, count)
	step := span / time.Duration(count)
	for i := 0; i < count; i++ {
		events = append(events, Event{Timestamp: start.Add(time.Duration(i) * step)})
	}
	return events
}

// THE INCIDENT, REPRODUCED TO THE REPORTED DIGIT. A daemon 23 seconds old, saturating its 2 req/s
// allowance for every one of those seconds, reported 7.7% on 2026-07-31 while Grafana and the rate
// limiter's own empty bucket both read ~100%.
//
// Against the unfixed ComputeDualReport this fails: 46 events / 300s / 2.0 = 7.67%.
func TestComputeDualReport_ASaturatedButYoungObserverDoesNotReportHeadroomItDoesNotHave(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	const observed = 23 * time.Second
	// 46 requests in 23s is exactly 2.0 req/s — the full account allowance, nothing spare.
	events := saturatingEvents(start, 46, observed)
	now := start.Add(observed)

	dual := ComputeDualReport(events, now, 2.0, start)

	// Non-vacuity: the events must all be inside the window, or a low reading would mean "counted
	// nothing" rather than "divided by the wrong span".
	if dual.Rolling5m.TotalRequests != 46 {
		t.Fatalf("counted %d of 46 events; this test is about the DENOMINATOR, so the numerator must be intact", dual.Rolling5m.TotalRequests)
	}
	if got := dual.Rolling5m.WindowSeconds; got != observed.Seconds() {
		t.Fatalf("Rolling5m window reported %.0fs after only %.0fs of observation; the report must narrow to the span observed, not claim 300s of history it never had", got, observed.Seconds())
	}
	if got := dual.Rolling5m.UtilizationPct; got < 99 || got > 101 {
		t.Fatalf("reported %.2f%% utilization for an account consuming its ENTIRE 2 req/s allowance for the whole observed span, want ~100%%. At 7.67%% the api_util money guard reads 92 points of headroom that does not exist and permits a hull purchase the account cannot fly", got)
	}
	if dual.Rolling5m.HeadroomReqPerSec > 0.02 {
		t.Fatalf("reported %.4f req/s of headroom on a fully saturated account", dual.Rolling5m.HeadroomReqPerSec)
	}
}

// ONCE WARM, NOTHING CHANGES. Past the nominal window the narrowing is inert, so steady-state
// behaviour — which is every reading more than five minutes after a restart — is byte-identical.
func TestComputeDualReport_AWarmObserverIsUnchanged(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	now := start.Add(2 * time.Hour)
	// 60 requests in the trailing 5 minutes: 0.2 req/s against a 2 req/s ceiling = 10%.
	events := saturatingEvents(now.Add(-5*time.Minute), 60, 5*time.Minute)

	dual := ComputeDualReport(events, now, 2.0, start)

	if got := dual.Rolling5m.WindowSeconds; got != 300 {
		t.Fatalf("window narrowed to %.0fs for an observer running two hours; narrowing must be inert once warm", got)
	}
	if got := dual.Rolling5m.UtilizationPct; got < 9.9 || got > 10.1 {
		t.Fatalf("reported %.2f%%, want ~10%% — a warm observer's arithmetic is untouched", got)
	}
}

// IDLE TIME IS OBSERVED TIME. This is the trap in deriving the span from the oldest retained EVENT
// instead of from the start of observation: after a quiet stretch the oldest event is recent, and a
// single request would divide by a couple of seconds and report saturation off one call.
//
// A guard that blocked fleet growth because one request happened recently would be as wrong as one
// that permitted it during saturation — just in the other direction.
func TestComputeDualReport_QuietTimeStaysInTheDenominator(t *testing.T) {
	start := time.Unix(0, 0).UTC()
	now := start.Add(time.Hour)
	// Warm observer, one solitary request two seconds ago, nothing before it in the window.
	events := []Event{{Timestamp: now.Add(-2 * time.Second)}}

	dual := ComputeDualReport(events, now, 2.0, start)

	if got := dual.Rolling5m.WindowSeconds; got != 300 {
		t.Fatalf("window collapsed to %.0fs around the only recent event; idle time is real observed time and belongs in the denominator", got)
	}
	// 1 request / 300s / 2.0 = 0.167%.
	if got := dual.Rolling5m.UtilizationPct; got > 1 {
		t.Fatalf("reported %.2f%% off a single request in five minutes; deriving the span from the oldest EVENT rather than the start of observation produces exactly this, and it would wedge fleet growth on an idle account", got)
	}
}

// THE NARROWING ONLY EVER RAISES UTILIZATION (RULINGS #4). The guard this figure feeds may become
// stricter, never more permissive — so no input may produce a window WIDER than the nominal one.
func TestObservedWindow_NeverWidensAndFallsBackToNominalOnDegenerateInput(t *testing.T) {
	now := time.Unix(10_000, 0).UTC()
	const nominal = 5 * time.Minute

	for _, tc := range []struct {
		name          string
		observedSince time.Time
		want          time.Duration
	}{
		{"unknown start falls back to nominal", time.Time{}, nominal},
		{"start in the future (clock skew) falls back", now.Add(time.Minute), nominal},
		{"start exactly now falls back", now, nominal},
		{"warm observer keeps nominal", now.Add(-time.Hour), nominal},
		{"exactly one window old keeps nominal", now.Add(-nominal), nominal},
		{"young observer narrows", now.Add(-30 * time.Second), 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := observedWindow(nominal, now, tc.observedSince)
			if got != tc.want {
				t.Fatalf("observedWindow = %v, want %v", got, tc.want)
			}
			if got > nominal {
				t.Fatalf("observedWindow returned %v, WIDER than the nominal %v — a wider window dilutes traffic and makes the money guard more permissive, which RULINGS #4 forbids", got, nominal)
			}
		})
	}
}
