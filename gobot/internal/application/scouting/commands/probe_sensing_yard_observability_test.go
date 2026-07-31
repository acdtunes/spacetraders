package commands

// THE TEST THAT WOULD HAVE CAUGHT IT, and the one shape that would not have.
//
// The yard counters were correct, were populated, and reached the heartbeat's
// structured payload on every tick — and the question they were built to answer
// still could not be answered, because nothing outside the process could read
// them (sp-qkskz). A test asserting `fields["yards_need_presence"] == 64` passes
// on the broken code: the map was always populated. That assertion IS the bug,
// written down as a check.
//
// So every test in this file reads its value back from a SINK — the bytes a
// Prometheus scrape returns, and the line the daemon prints to daemon.log —
// never from the payload map on the way in. If a change deletes the exposition
// or stops the renderer emitting fields, these fail; if it merely renames a
// struct field, the compiler already had that covered.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	adapters "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The numbers one tick writes. Deliberately distinct from each other and from
// any plausible default, so an assertion cannot pass on a value that arrived by
// coincidence — a fixture of all 1s would let "publishes SOMETHING" masquerade
// as "publishes THIS".
const (
	tickYardsOutstanding = 37
	tickYardsRead        = 11
	tickYardsFailed      = 3

	tickPresenceRequested  = 64
	tickPresenceDispatched = 9
	tickPresenceNoHull     = 41
	tickPresenceMetered    = 14

	tickYardSlotsQueued = 71
	tickYardSlotsAtHead = 6
	tickYardSlotsFilled = 2
)

// liveTick is one heartbeat carrying the numbers above.
func liveTick() heartbeat {
	return heartbeat{
		yard: parkedsensing.YardCatalogReport{
			Outstanding: tickYardsOutstanding,
			Read:        tickYardsRead,
			Failed:      tickYardsFailed,
		},
		presence: parkedsensing.YardPresenceReport{
			Requested:  tickPresenceRequested,
			Dispatched: tickPresenceDispatched,
			NoHull:     tickPresenceNoHull,
			Metered:    tickPresenceMetered,
		},
		buy: parkedsensing.BuyReport{
			YardsQueued: tickYardSlotsQueued,
			YardsAtHead: tickYardSlotsAtHead,
			YardsFilled: tickYardSlotsFilled,
		},
	}
}

// renderingLogger is the production sink, not a stand-in for it. It formats
// through logging.FormatLine — the exact call ContainerRunner.Log makes — so a
// change that stops the daemon printing structured fields fails here too.
type renderingLogger struct{ lines []string }

func (l *renderingLogger) Log(level, message string, metadata map[string]interface{}) {
	l.lines = append(l.lines, logging.FormatLine(
		time.Date(2026, 7, 30, 23, 13, 2, 0, time.UTC),
		"probe_sensing_coordinator-player-5-bb435635", level, message, metadata))
}

// scrape drives one heartbeat through the REAL adapter chain — MetricsPort, the
// global collector, a live registry — and returns what an HTTP scrape of :9090
// would serve, in the Prometheus text exposition format. Nothing here is a
// double: the only substitution is the registry, so the test reads its own
// process instead of the daemon's.
func scrape(t *testing.T, playerID int, hb heartbeat) string {
	t.Helper()

	registry := prometheus.NewRegistry()
	previousRegistry, previousCollector := metrics.Registry, metrics.GetGlobalParkedSensingCollector()
	metrics.Registry = registry
	t.Cleanup(func() {
		metrics.Registry = previousRegistry
		metrics.SetGlobalParkedSensingCollector(previousCollector)
	})

	collector := metrics.NewParkedSensingMetricsCollector()
	if err := collector.Register(); err != nil {
		t.Fatalf("register the sensing collector: %v", err)
	}
	metrics.SetGlobalParkedSensingCollector(collector)

	h := &RunProbeSensingCoordinatorHandler{}
	h.SetMetricsRecorder(adapters.NewMetricsPort())
	h.heartbeat(common.WithLogger(context.Background(), &renderingLogger{}),
		&RunProbeSensingCoordinatorCommand{
			PlayerID:    shared.MustNewPlayerID(playerID),
			ContainerID: "probe_sensing_coordinator-player-5-bb435635",
		},
		sensingConfig{ProbeCap: 800}, hb)

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather the registry: %v", err)
	}
	var rendered bytes.Buffer
	encoder := expfmt.NewEncoder(&rendered, expfmt.FmtText)
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			t.Fatalf("encode %s: %v", family.GetName(), err)
		}
	}
	if rendered.Len() == 0 {
		t.Fatal("the scrape surface is empty, so no assertion below could distinguish " +
			"a missing gauge from a missing fixture")
	}
	return rendered.String()
}

// THE FALSIFIER. A non-zero value this tick wrote is readable from outside the
// process, on the channel a Grafana panel and an alert rule actually read.
//
// This fails on main with "metric absent": before this change the complete set
// of sensing metrics was four, and not one of them was a yard counter.
func TestYardCounters_AreReadableFromAScrape(t *testing.T) {
	exposition := scrape(t, 5, liveTick())

	for _, want := range []string{
		`spacetraders_daemon_parked_sensing_yard_presence{outcome="requested",player_id="5"} 64`,
		`spacetraders_daemon_parked_sensing_yard_presence{outcome="dispatched",player_id="5"} 9`,
		`spacetraders_daemon_parked_sensing_yard_presence{outcome="no_hull",player_id="5"} 41`,
		`spacetraders_daemon_parked_sensing_yard_presence{outcome="metered",player_id="5"} 14`,

		`spacetraders_daemon_parked_sensing_yard_slots{player_id="5",stage="queued"} 71`,
		`spacetraders_daemon_parked_sensing_yard_slots{player_id="5",stage="at_head"} 6`,
		`spacetraders_daemon_parked_sensing_yard_slots{player_id="5",stage="filled"} 2`,

		`spacetraders_daemon_parked_sensing_yard_catalogue{player_id="5",state="outstanding"} 37`,
		`spacetraders_daemon_parked_sensing_yard_catalogue{player_id="5",state="read"} 11`,
		`spacetraders_daemon_parked_sensing_yard_catalogue{player_id="5",state="failed"} 3`,
	} {
		if !strings.Contains(exposition, want) {
			t.Errorf("a scrape does not carry %q\n"+
				"The counter is computed and reaches the log payload either way, so an operator "+
				"cannot tell a pass that is dispatching and finding no work from one that never "+
				"ran (sp-qkskz). Exposition was:\n%s", want, exposition)
		}
	}
}

// The three passes fail INDEPENDENTLY, so they must be readable independently.
// A single collapsed "yards" number reads the same whether the catalogue sweep
// stalled, presence was starved of hulls, or the buy queue ranked dark yards
// last — which is the distinction the counters exist to draw.
func TestYardCounters_SeparateTheThreePassesThatFailIndependently(t *testing.T) {
	exposition := scrape(t, 5, liveTick())

	for _, family := range []string{
		"spacetraders_daemon_parked_sensing_yard_catalogue",
		"spacetraders_daemon_parked_sensing_yard_presence",
		"spacetraders_daemon_parked_sensing_yard_slots",
	} {
		if !strings.Contains(exposition, "# TYPE "+family+" gauge") {
			t.Errorf("%s is not on the scrape surface at all", family)
		}
	}
}

// A ZERO IS A READING, NOT AN ABSENCE. A gauge that stops reporting leaves its
// last non-zero value standing in Prometheus until the series goes stale, so a
// backlog that drained to nothing would read as permanently jammed — the exact
// misreading these counters exist to prevent, restored one layer down. Every
// label value is republished every tick, zeros included.
func TestYardCounters_PublishZerosRatherThanGoingSilent(t *testing.T) {
	exposition := scrape(t, 5, heartbeat{})

	for _, want := range []string{
		`spacetraders_daemon_parked_sensing_yard_presence{outcome="requested",player_id="5"} 0`,
		`spacetraders_daemon_parked_sensing_yard_presence{outcome="dispatched",player_id="5"} 0`,
		`spacetraders_daemon_parked_sensing_yard_presence{outcome="no_hull",player_id="5"} 0`,
		`spacetraders_daemon_parked_sensing_yard_presence{outcome="metered",player_id="5"} 0`,
		`spacetraders_daemon_parked_sensing_yard_slots{player_id="5",stage="queued"} 0`,
		`spacetraders_daemon_parked_sensing_yard_slots{player_id="5",stage="at_head"} 0`,
		`spacetraders_daemon_parked_sensing_yard_slots{player_id="5",stage="filled"} 0`,
		`spacetraders_daemon_parked_sensing_yard_catalogue{player_id="5",state="outstanding"} 0`,
		`spacetraders_daemon_parked_sensing_yard_catalogue{player_id="5",state="read"} 0`,
		`spacetraders_daemon_parked_sensing_yard_catalogue{player_id="5",state="failed"} 0`,
	} {
		if !strings.Contains(exposition, want) {
			t.Errorf("an idle tick does not publish %q\n"+
				"A silent gauge holds its last non-zero value, so a drained backlog would read "+
				"as permanently jammed. Exposition was:\n%s", want, exposition)
		}
	}
}

// The gauges must survive the tick that most needs them. publish() sits inside
// the rotation-view branch and is skipped whenever that read fails; the yard
// gauges are published from the heartbeat, which always runs, so a tick with no
// rotation still reports its yard numbers.
func TestYardCounters_SurviveATickWithNoRotationView(t *testing.T) {
	tick := liveTick()
	tick.rotation = 0 // as a failed ParkedSlotViews read leaves it

	if !strings.Contains(scrape(t, 5, tick),
		`spacetraders_daemon_parked_sensing_yard_presence{outcome="requested",player_id="5"} 64`) {
		t.Fatal("a tick that could not read the rotation published no yard backlog — " +
			"that is the tick an operator most needs it on")
	}
}

// Observation must never be able to fail a reconcile (RULINGS #4). An unwired
// recorder is the daemon's own boot-order state, not a test-only case.
func TestYardCounters_UnwiredRecorderIsSilentRatherThanFatal(t *testing.T) {
	log := &renderingLogger{}
	h := &RunProbeSensingCoordinatorHandler{} // no recorder

	h.heartbeat(common.WithLogger(context.Background(), log),
		&RunProbeSensingCoordinatorCommand{PlayerID: shared.MustNewPlayerID(5), ContainerID: "c"},
		sensingConfig{ProbeCap: 800}, liveTick())

	if len(log.lines) != 1 {
		t.Fatalf("want the cycle line to be emitted anyway, got %d lines", len(log.lines))
	}
}

// THE SECOND SINK. daemon.log is where an operator looks first, and a grep for
// any field name on it used to return nothing — not for the yard counters, and
// not for long-standing fields like yards_read or probe_cap either. This reads
// the rendered line, so it fails on main: the format string names none of these.
func TestYardCounters_AreReadableInTheRenderedLogLine(t *testing.T) {
	log := &renderingLogger{}
	h := &RunProbeSensingCoordinatorHandler{}

	h.heartbeat(common.WithLogger(context.Background(), log),
		&RunProbeSensingCoordinatorCommand{PlayerID: shared.MustNewPlayerID(5), ContainerID: "c"},
		sensingConfig{ProbeCap: 800}, liveTick())

	if len(log.lines) != 1 {
		t.Fatalf("want exactly one cycle line, got %d", len(log.lines))
	}
	line := log.lines[0]

	for _, want := range []string{
		`"yards_need_presence":64`,
		`"yard_slots_queued":71`,
		`"yards_presence_sent":9`,
		`"yards_presence_nohull":41`,
		`"yard_slots_at_head":6`,
		// The pre-existing fields the new counters inherited the blind spot from.
		// They were invisible for far longer and are fixed by the same change.
		`"yards_read":11`,
		`"probe_cap":800`,
		`"action":"parked_sensing_cycle"`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the rendered cycle line does not carry %s\nline was:\n%s", want, line)
		}
	}
}

// The message keeps its shape. Every standing grep against daemon.log matches
// the human-readable half, and the fields are a SUFFIX — restoring the payload
// must not cost the line an operator already knows how to read.
func TestRenderedCycleLine_KeepsTheHumanReadableMessageInFront(t *testing.T) {
	log := &renderingLogger{}
	h := &RunProbeSensingCoordinatorHandler{}

	h.heartbeat(common.WithLogger(context.Background(), log),
		&RunProbeSensingCoordinatorCommand{PlayerID: shared.MustNewPlayerID(5), ContainerID: "c"},
		sensingConfig{ProbeCap: 800}, liveTick())

	line := log.lines[0]
	message := strings.Index(line, "Parked sensing cycle:")
	fields := strings.Index(line, `{"action"`)
	if message < 0 || fields < 0 {
		t.Fatalf("want both halves on the line, got:\n%s", line)
	}
	if message > fields {
		t.Fatalf("the fields precede the message, breaking every standing grep:\n%s", line)
	}
}
