package commands

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
)

// anchoredEraRoles is an era whose charted template produced all four standby anchors. The two
// central anchors are also central parks (they are charted marketplaces), which is what the
// fill must not hand out twice.
func anchoredEraRoles() (contractscaler.EraRoles, map[string]float64) {
	roles := contractscaler.EraRoles{
		CentralParks: []string{"HSTACK", "ESTACK", "RICH", "MID"},
		FarSink:      "FARSINK",
		Anchors: contractscaler.EraAnchors{
			HStack:        "HSTACK",
			FarSink:       "FARSINK",
			FarSourceBase: "FARBASE",
			EStack:        "ESTACK",
		},
	}
	return roles, map[string]float64{"RICH": 90, "MID": 50, "HSTACK": 5, "ESTACK": 4}
}

// The scaler's armed standby set is the SAME anchor-ordered selection the coordinator's homing
// resolves — one slot set, no drift — so a bought hull is homed to the era-invariant anchor its
// buy was planned against, not to the top-demand central park.
func TestReconcile_BuyOrderCarriesTheEraInvariantAnchorPlacementOrder(t *testing.T) {
	h, pur, _, rr := newHarness(1)
	rr.roles, rr.demand = anchoredEraRoles()

	reconcile(t, h, 1)

	if len(pur.orders) != 1 {
		t.Fatalf("orders = %d, want 1", len(pur.orders))
	}
	want := []string{"HSTACK", "FARSINK", "FARBASE", "ESTACK", "RICH", "MID"}
	if !reflect.DeepEqual(pur.orders[0].StandbyStations, want) {
		t.Fatalf("order StandbyStations = %v, want the anchors in placement order then the demand-ranked fill %v",
			pur.orders[0].StandbyStations, want)
	}
}

// A slot that fails open does so SILENTLY, so a changed generator template must be LOGGED or it
// is indistinguishable from a healthy era and nobody re-ranks the slot from the corpus. Logged
// once per arm (the plan is memoized), naming exactly the anchors this era did not chart.
func TestArmedPlan_LogsTheAnchorsThisEraDidNotChart(t *testing.T) {
	h, _, _, rr := newHarness(1)
	rr.roles, rr.demand = anchoredEraRoles()
	rr.roles.Anchors.FarSourceBase = "" // this era charted no OUTPOST asteroid base
	rr.roles.Anchors.EStack = ""        // …and no station-free planet+moon stack

	logger := &captureLogger{}
	ctx := logging.WithLogger(context.Background(), logger)
	cmd := &RunContractScalerCommand{PlayerID: 1, ContainerID: "cs-1"}
	for arm := 0; arm < 3; arm++ {
		if _, err := h.armedPlanFor(ctx, cmd); err != nil {
			t.Fatalf("armedPlanFor: %v", err)
		}
	}

	misses := logger.withAction("contract_standby_anchor_miss")
	if len(misses) != 1 {
		t.Fatalf("anchor-miss logs = %d, want exactly 1 (the plan arms once)", len(misses))
	}
	if misses[0]["level"] != "WARN" {
		t.Fatalf("anchor-miss level = %v, want WARN", misses[0]["level"])
	}
	want := []string{contractscaler.AnchorFarSourceBase, contractscaler.AnchorEStack}
	if !reflect.DeepEqual(misses[0]["missing_anchors"], want) {
		t.Fatalf("missing_anchors = %v, want %v (placement order)", misses[0]["missing_anchors"], want)
	}
}

// A fully charted era logs no miss — the WARN means something.
func TestArmedPlan_LogsNoMissWhenTheEraChartedEveryAnchor(t *testing.T) {
	h, _, _, rr := newHarness(1)
	rr.roles, rr.demand = anchoredEraRoles()

	logger := &captureLogger{}
	if _, err := h.armedPlanFor(logging.WithLogger(context.Background(), logger), &RunContractScalerCommand{PlayerID: 1, ContainerID: "cs-1"}); err != nil {
		t.Fatalf("armedPlanFor: %v", err)
	}

	if got := logger.withAction("contract_standby_anchor_miss"); len(got) != 0 {
		t.Fatalf("anchor-miss logs = %v, want none on a fully charted era", got)
	}
}

type captureLogger struct {
	mu    sync.Mutex
	lines []map[string]interface{}
}

func (l *captureLogger) Log(level, message string, metadata map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := map[string]interface{}{"level": level, "message": message}
	for key, value := range metadata {
		line[key] = value
	}
	l.lines = append(l.lines, line)
}

func (l *captureLogger) withAction(action string) []map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	var hits []map[string]interface{}
	for _, line := range l.lines {
		if line["action"] == action {
			hits = append(hits, line)
		}
	}
	return hits
}
