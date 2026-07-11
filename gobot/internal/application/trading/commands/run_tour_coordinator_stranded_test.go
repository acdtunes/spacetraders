package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// strandedMsgCount counts how many captured log messages contain sub — used to prove the
// stranded WARN fires exactly ONCE per episode, not once per launch (sp-686e).
func strandedMsgCount(msgs []string, sub string) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

// sp-686e: two consecutive qualifying empties are NOT yet a stranded episode; the third
// (default threshold, passed as 0 => 3) is — and the emit signal fires on exactly that
// transition. This is the core TORWIND-2C counting contract.
func TestStranded_TwoEmptiesNotEpisode_ThreeIs(t *testing.T) {
	h := newTourHandler(t, repositionFixture(), &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	if h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 0) {
		t.Fatal("1st qualifying empty must not be an episode")
	}
	if h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 0) {
		t.Fatal("2nd qualifying empty must not be an episode")
	}
	if !h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 0) {
		t.Fatal("3rd consecutive qualifying empty must cross the default threshold into an episode")
	}
}

// sp-686e: the WARN/metric fires ONCE per episode — the 4th+ qualifying empty keeps the
// hull stranded but must NOT re-emit (state-change de-dup, the ikx1/13tl idiom).
func TestStranded_OncePerEpisode(t *testing.T) {
	h := newTourHandler(t, repositionFixture(), &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	emits := 0
	for i := 0; i < 6; i++ {
		if h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 3) {
			emits++
		}
	}
	if emits != 1 {
		t.Fatalf("6 consecutive qualifying empties must emit exactly ONE episode, got %d", emits)
	}
}

// sp-686e: any successful discovery (a non-qualifying outcome — candidates found, or an
// empty with reachable neighbors) RESETS the consecutive streak, and the counter re-arms
// from zero for a fresh episode afterward.
func TestStranded_ResetOnSuccessfulDiscovery(t *testing.T) {
	h := newTourHandler(t, repositionFixture(), &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 3) // 1
	h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 3) // 2
	if h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", false, 3) {
		t.Fatal("a successful discovery must never emit an episode")
	}
	if h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 3) {
		t.Fatal("streak must reset after a successful discovery — 1st post-reset empty is not an episode")
	}
	if h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 3) {
		t.Fatal("2nd post-reset empty is still not an episode")
	}
	if !h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 3) {
		t.Fatal("3rd post-reset empty must be a fresh episode")
	}
}

// sp-686e: the threshold is config-driven (RULINGS #5). A configured value of 2 trips on
// the second consecutive qualifying empty, not the default third.
func TestStranded_RespectsConfiguredThreshold(t *testing.T) {
	h := newTourHandler(t, repositionFixture(), &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	if h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 2) {
		t.Fatal("1st empty must not trip a threshold of 2")
	}
	if !h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 2) {
		t.Fatal("2nd empty must trip a configured threshold of 2")
	}
}

// sp-686e: a stranded streak is scoped to the system it accrues at — if the hull is at a
// DIFFERENT system (a captain-authority extraction that landed it on another dead ground),
// that is a fresh episode, not a continuation of the old count.
func TestStranded_SystemChangeStartsNewEpisode(t *testing.T) {
	h := newTourHandler(t, repositionFixture(), &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 3) // A:1
	h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-PD21", true, 3) // A:2
	if h.noteRepositionStrandedDiscovery("TORWIND-2C", "X1-OTHER", true, 3) {
		t.Fatal("a qualifying empty at a NEW system starts a fresh episode (count 1), not a continuation to 3")
	}
}

// sp-686e integration: the TORWIND-2C shape end-to-end. An origin-level empty discovery
// whose reason is no-durable-adjacency (the durable gate graph reports zero edges AND the
// live scan is empty) increments the per-hull streak through the real buildRepositionCandidates
// path, and the 3rd consecutive one emits exactly ONE greppable stranded WARN naming the
// ship, system, and bead. The 4th does not re-emit (once per episode).
func TestReposition_StrandedShape_WarnsOncePerEpisode(t *testing.T) {
	fx := repositionFixture()
	fx.neighbors = map[string][]string{} // live scan empty (uncharted-origin shape)
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{}}) // zero durable adjacency

	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TORWIND-2C", PlayerID: 1, StrandedConsecutiveThreshold: 3}

	h.buildRepositionCandidates(ctx, cmd, "X1-PD21")
	h.buildRepositionCandidates(ctx, cmd, "X1-PD21")
	if strandedMsgCount(logger.messages, "hull stranded") != 0 {
		t.Fatalf("2 consecutive stranded empties must NOT emit (threshold 3):\n%s", strings.Join(logger.messages, "\n"))
	}
	h.buildRepositionCandidates(ctx, cmd, "X1-PD21")
	if n := strandedMsgCount(logger.messages, "hull stranded"); n != 1 {
		t.Fatalf("3rd consecutive stranded empty must emit exactly ONE WARN, got %d:\n%s", n, strings.Join(logger.messages, "\n"))
	}
	if !logger.loggedContaining("hull stranded", "TORWIND-2C", "X1-PD21", "sp-686e") {
		t.Fatalf("stranded WARN must name the ship, system, and bead:\n%s", strings.Join(logger.messages, "\n"))
	}
	h.buildRepositionCandidates(ctx, cmd, "X1-PD21")
	if n := strandedMsgCount(logger.messages, "hull stranded"); n != 1 {
		t.Fatalf("stranded WARN must fire once per episode, not per launch, got %d", n)
	}
}

// sp-686e: a NON-stranded empty — neighbors WERE resolved but each fell out (here an
// under-construction gate) — is transient, not the TORWIND-2C structural strand, so it
// must never count toward a stranded episode no matter how many times it repeats. This
// pins the qualifying predicate to len(neighbors)==0, guarding against a false page for a
// hull that genuinely has reachable (merely momentarily unusable) neighbors.
func TestReposition_TransientEmpty_NeverCountsAsStranded(t *testing.T) {
	fx := repositionFixture()
	fx.neighbors = map[string][]string{}
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-PD21": {{ConnectedSystem: "X1-S2", GateWaypoint: "X1-S2-GATE", UnderConstruction: true}},
	}})
	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TORWIND-2C", PlayerID: 1, StrandedConsecutiveThreshold: 3}
	for i := 0; i < 5; i++ {
		h.buildRepositionCandidates(ctx, cmd, "X1-PD21")
	}
	if strandedMsgCount(logger.messages, "hull stranded") != 0 {
		t.Fatalf("an empty with REACHABLE (merely unbuildable) neighbors is not the stranded shape and must never page:\n%s", strings.Join(logger.messages, "\n"))
	}
}
