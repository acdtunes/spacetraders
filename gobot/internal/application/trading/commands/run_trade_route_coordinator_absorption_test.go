package commands

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-78ai L4: scanLanes' read-only absorption-ledger consult. Trade-analyst Q1
// ruling: circuits are READ-ONLY consumers of the ledger — these tests assert
// the write methods on the fake ledger are NEVER called, since the handler under
// test has no write path at all. The single-lane WIDGET fixture (X1-HOME-A
// export -> X1-HOME-B import, VolumeCap 100) keeps the consult's verdict logic
// isolated from the unrelated multi-system/gate-penalty behavior already
// covered in run_trade_route_coordinator_multisystem_test.go.

// widgetLaneMarketRepo builds the one-lane WIDGET fixture shared by every test
// in this file: source X1-HOME-A (ask 100) -> dest X1-HOME-B (bid 600), both
// sides volume 100 so VolumeCap == 100 and the reserved-depth math below has a
// clean denominator.
func widgetLaneMarketRepo() *msMarketRepo {
	return &msMarketRepo{
		waypointsBySystem: map[string][]string{
			"X1-HOME": {"X1-HOME-A", "X1-HOME-B"},
		},
		goods: map[string]msGood{
			"X1-HOME-A": {symbol: "WIDGET", bid: 50, ask: 100, volume: 100, tradeType: market.TradeTypeExport},
			"X1-HOME-B": {symbol: "WIDGET", bid: 600, ask: 650, volume: 100, tradeType: market.TradeTypeImport},
		},
	}
}

// widgetSellKey is the LaneKey the consult looks up for the fixture's lane: the
// SELL side at the destination (design §2 — circuits only consult the sink
// they'd dump into).
var widgetSellKey = absorption.LaneKey{Waypoint: "X1-HOME-B", Good: "WIDGET", Side: absorption.SideSell}

// absorptionFakeLedger is a minimal in-memory absorption.Ledger test double for
// the scanLanes consult: Outstanding serves preset pools (or outErr, to force
// the fail-closed path); every write method is instrumented so a test can
// assert it was NEVER called — the read-only contract (trade-analyst Q1).
type absorptionFakeLedger struct {
	mu     sync.Mutex
	pools  map[absorption.LaneKey]absorption.KeyOccupancy
	outErr error

	reserveCalls       int
	recordPlannedCalls int
	convertCalls       int
	releaseCalls       int
}

func newAbsorptionFakeLedger() *absorptionFakeLedger {
	return &absorptionFakeLedger{pools: map[absorption.LaneKey]absorption.KeyOccupancy{}}
}

func (f *absorptionFakeLedger) preset(k absorption.LaneKey, occ absorption.KeyOccupancy) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pools[k] = occ
}

func (f *absorptionFakeLedger) Outstanding(context.Context, int) (map[absorption.LaneKey]absorption.KeyOccupancy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.outErr != nil {
		return nil, f.outErr
	}
	out := make(map[absorption.LaneKey]absorption.KeyOccupancy, len(f.pools))
	for k, v := range f.pools {
		out[k] = v
	}
	return out, nil
}

func (f *absorptionFakeLedger) Reserve(context.Context, int, string, string, []absorption.ReserveEntry) ([]string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserveCalls++
	return nil, true, nil
}

func (f *absorptionFakeLedger) RecordPlanned(context.Context, int, string, string, absorption.ReserveEntry) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordPlannedCalls++
	return "", nil
}

func (f *absorptionFakeLedger) ConvertByContainer(context.Context, string, int, absorption.LaneKey, int, string, int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convertCalls++
	return nil
}

func (f *absorptionFakeLedger) Release(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	return nil
}

func (f *absorptionFakeLedger) writesCalled() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reserveCalls + f.recordPlannedCalls + f.convertCalls + f.releaseCalls
}

var _ absorption.Ledger = (*absorptionFakeLedger)(nil)

// entriesWithPrefix filters a laneLogCapturingLogger's captured entries by
// message prefix — the absorption consult's own lines, distinct from the
// pre-existing lane-selection logging run_trade_route_coordinator_lanelog_test.go
// asserts on.
func entriesWithPrefix(logger *laneLogCapturingLogger, prefix string) []laneLogEntry {
	var found []laneLogEntry
	for _, e := range logger.entries {
		if strings.HasPrefix(e.message, prefix) {
			found = append(found, e)
		}
	}
	return found
}

// 1. A lane whose sell side carries an active recovering shadow above the floor
// is excluded, independent of how much depth remains — the shadow check fires
// BEFORE the reserved-depth math (design §2).
func TestScanLanes_Absorption_ShadowedSellSide_Excluded(t *testing.T) {
	handler := NewRunTradeRouteCoordinatorHandler(&msMediator{}, nil, widgetLaneMarketRepo(), nil, nil, nil)
	ledger := newAbsorptionFakeLedger()
	ledger.preset(widgetSellKey, absorption.KeyOccupancy{RecoveringResidual: 5, PlannedUnits: 0})
	handler.SetAbsorptionLedger(ledger, false)

	logger := &laneLogCapturingLogger{}
	lanes, err := handler.scanLanes(common.WithLogger(context.Background(), logger), "X1-HOME", 1, 40, "")
	if err != nil {
		t.Fatalf("expected no error (fail-closed excludes, it doesn't abort the scan), got: %v", err)
	}
	if len(lanes) != 0 {
		t.Fatalf("expected the shadowed lane excluded, got %d lanes: %+v", len(lanes), lanes)
	}
	excluded := entriesWithPrefix(logger, "Trade-route absorption consult: excluded lane")
	if len(excluded) != 1 {
		t.Fatalf("expected exactly one exclusion log line, got %d: %+v", len(excluded), excluded)
	}
	if !strings.Contains(excluded[0].message, "verdict shadow") {
		t.Fatalf("expected verdict shadow in the log line, got: %s", excluded[0].message)
	}
	if excluded[0].metadata["verdict"] != "shadow" || excluded[0].metadata["good"] != "WIDGET" {
		t.Fatalf("expected structured verdict=shadow good=WIDGET metadata, got: %+v", excluded[0].metadata)
	}
	if ledger.writesCalled() != 0 {
		t.Fatalf("READ-ONLY (trade-analyst Q1): the consult must never write to the ledger, got %d write calls", ledger.writesCalled())
	}
}

// 2. A shadow that has recovered past its floor (Outstanding already reports
// zero residual — the server-side floor filter in AbsorptionLedgerGORM.Outstanding
// has done its job) does not block; the lane ranks exactly as it would with no
// ledger at all.
func TestScanLanes_Absorption_RecoveredPastFloor_RanksNormally(t *testing.T) {
	handler := NewRunTradeRouteCoordinatorHandler(&msMediator{}, nil, widgetLaneMarketRepo(), nil, nil, nil)
	ledger := newAbsorptionFakeLedger()
	ledger.preset(widgetSellKey, absorption.KeyOccupancy{RecoveringResidual: 0, PlannedUnits: 0})
	handler.SetAbsorptionLedger(ledger, false)

	lanes, err := handler.scanLanes(context.Background(), "X1-HOME", 1, 40, "")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(lanes) != 1 || lanes[0].Good != "WIDGET" || lanes[0].DestWaypoint != "X1-HOME-B" {
		t.Fatalf("expected the recovered lane to rank normally, got %+v", lanes)
	}
}

// 3. No live shadow, but another engine's PLANNED units have already claimed
// enough of the lane's depth that what remains can't fill a circuit tranche
// (min(VolumeCap, shipCapacity)) — excluded with verdict reserved-depth.
// VolumeCap=100, shipCapacity=40 -> circuitTranche=40; PlannedUnits=65 leaves
// remaining=35 < 40.
func TestScanLanes_Absorption_ReservedDepthExhausted_Excluded(t *testing.T) {
	handler := NewRunTradeRouteCoordinatorHandler(&msMediator{}, nil, widgetLaneMarketRepo(), nil, nil, nil)
	ledger := newAbsorptionFakeLedger()
	ledger.preset(widgetSellKey, absorption.KeyOccupancy{PlannedUnits: 65})
	handler.SetAbsorptionLedger(ledger, false)

	logger := &laneLogCapturingLogger{}
	lanes, err := handler.scanLanes(common.WithLogger(context.Background(), logger), "X1-HOME", 1, 40, "")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(lanes) != 0 {
		t.Fatalf("expected the reserved-depth-exhausted lane excluded, got %d lanes: %+v", len(lanes), lanes)
	}
	excluded := entriesWithPrefix(logger, "Trade-route absorption consult: excluded lane")
	if len(excluded) != 1 || !strings.Contains(excluded[0].message, "verdict reserved-depth") {
		t.Fatalf("expected one exclusion line with verdict reserved-depth, got: %+v", excluded)
	}
}

// 4. Fail-closed read: an unreadable ledger excludes every shadow-consult-
// eligible lane this scan (RULINGS #4) — it does not abort scanLanes itself
// (the pre-existing bid-floor stop and lane behavior remain the hard guards).
func TestScanLanes_Absorption_LedgerUnreadable_FailsClosed(t *testing.T) {
	handler := NewRunTradeRouteCoordinatorHandler(&msMediator{}, nil, widgetLaneMarketRepo(), nil, nil, nil)
	ledger := newAbsorptionFakeLedger()
	ledger.outErr = fmt.Errorf("ledger unreachable")
	handler.SetAbsorptionLedger(ledger, false)

	logger := &laneLogCapturingLogger{}
	lanes, err := handler.scanLanes(common.WithLogger(context.Background(), logger), "X1-HOME", 1, 40, "")
	if err != nil {
		t.Fatalf("an unreadable ledger must fail closed (exclude), not abort the scan: %v", err)
	}
	if len(lanes) != 0 {
		t.Fatalf("expected every eligible lane excluded, got %d lanes: %+v", len(lanes), lanes)
	}
	warnings := entriesWithPrefix(logger, "Trade-route absorption consult: ledger read failed")
	if len(warnings) != 1 || warnings[0].level != "WARNING" {
		t.Fatalf("expected one WARNING about the failed read, got: %+v", warnings)
	}
	excluded := entriesWithPrefix(logger, "Trade-route absorption consult: excluded lane")
	if len(excluded) != 1 || !strings.Contains(excluded[0].message, "verdict unreadable") {
		t.Fatalf("expected one exclusion line with verdict unreadable, got: %+v", excluded)
	}
}

// 5. The kill-switch (consultDisabled=true) suppresses the consult entirely —
// ranking must come back byte-identical to a nil-ledger scan, even though the
// preset ledger state (a live shadow) would exclude the lane if the consult
// were active. No absorption log line of any kind is emitted while killed.
func TestScanLanes_Absorption_KillSwitch_RestoresRankingByteIdentically(t *testing.T) {
	baselineHandler := NewRunTradeRouteCoordinatorHandler(&msMediator{}, nil, widgetLaneMarketRepo(), nil, nil, nil)
	baseline, err := baselineHandler.scanLanes(context.Background(), "X1-HOME", 1, 40, "")
	if err != nil {
		t.Fatalf("expected no error computing the baseline, got: %v", err)
	}

	killedHandler := NewRunTradeRouteCoordinatorHandler(&msMediator{}, nil, widgetLaneMarketRepo(), nil, nil, nil)
	ledger := newAbsorptionFakeLedger()
	ledger.preset(widgetSellKey, absorption.KeyOccupancy{RecoveringResidual: 999}) // would fully block if active
	killedHandler.SetAbsorptionLedger(ledger, true /* consultDisabled */)

	logger := &laneLogCapturingLogger{}
	killed, err := killedHandler.scanLanes(common.WithLogger(context.Background(), logger), "X1-HOME", 1, 40, "")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !reflect.DeepEqual(baseline, killed) {
		t.Fatalf("kill-switch must restore ranking byte-identically:\nbaseline=%+v\nkilled=  %+v", baseline, killed)
	}
	if len(entriesWithPrefix(logger, "Trade-route absorption consult:")) != 0 {
		t.Fatalf("expected zero absorption-consult log lines while the kill-switch is set, got: %+v", logger.entries)
	}
}

// 6. Regression: a nil ledger (no SetAbsorptionLedger call at all) leaves the
// consult fully inert — pre-L4 behavior, byte for byte. This is the same
// contract the SetGateGraph/SetEventSubscriber optional ports already use.
func TestScanLanes_Absorption_NilLedger_Inert(t *testing.T) {
	handler := NewRunTradeRouteCoordinatorHandler(&msMediator{}, nil, widgetLaneMarketRepo(), nil, nil, nil)

	logger := &laneLogCapturingLogger{}
	lanes, err := handler.scanLanes(common.WithLogger(context.Background(), logger), "X1-HOME", 1, 40, "")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(lanes) != 1 || lanes[0].Good != "WIDGET" {
		t.Fatalf("expected the lane to rank normally with no ledger wired, got %+v", lanes)
	}
	if len(entriesWithPrefix(logger, "Trade-route absorption consult:")) != 0 {
		t.Fatalf("expected zero absorption-consult log lines with no ledger wired, got: %+v", logger.entries)
	}
}
