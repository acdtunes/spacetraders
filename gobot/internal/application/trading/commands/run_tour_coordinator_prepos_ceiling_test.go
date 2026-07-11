package commands

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	persistence "github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// --- sp-13tl: pre-positioning capital-ceiling enablement + parked-log de-dup ---
//
// These tests pin the two halves of sp-13tl at the depositCandidates seam:
//  1. ENABLEMENT: a 0/absent capital ceiling PARKS opportunistic tour deposits
//     (fail closed, dormant) instead of silently defaulting to 10% of treasury —
//     money movement is a captain/analyst decision (RULINGS #5). The funnel
//     (BuildDepositCandidates -> warehouse finder) is NOT entered while parked, so
//     the finder call-count is the "capital binds vs funnel runs" oracle.
//  2. LOG CALM: a hull whose deposits stay parked across many re-plans logs ONE line
//     per container per distinct state, never one per re-plan (the deploy-time spam).

// ppCeilSpyFinder records FindRunning calls so a test can prove whether the deposit
// funnel was entered (capital available) or short-circuited before it (capital binds).
type ppCeilSpyFinder struct {
	mu    sync.Mutex
	calls int
	ops   []*storage.StorageOperation
}

func (f *ppCeilSpyFinder) FindRunning(_ context.Context, _ int) ([]*storage.StorageOperation, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.ops, nil
}

func (f *ppCeilSpyFinder) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ppCeilNoopMiner is a non-nil demand miner so the "subsystem wired" precondition
// passes; the parked paths short-circuit before it is ever consulted.
type ppCeilNoopMiner struct{}

func (ppCeilNoopMiner) Mine(_ context.Context, _ string, _ int, _ *int, _ persistence.DemandMinerOptions) ([]persistence.DemandCandidate, error) {
	return nil, nil
}

// ppCeilAPI is a minimal live-treasury fake. credits is a fixed balance; seq drives a
// sequence of balances across successive GetAgent calls (last value sticks) for the
// state-change test; err simulates an unreadable balance.
type ppCeilAPI struct {
	domainPorts.APIClient
	mu      sync.Mutex
	credits int
	seq     []int
	err     bool
	calls   int
}

func (c *ppCeilAPI) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err {
		return nil, errors.New("agent API unavailable")
	}
	credits := c.credits
	if len(c.seq) > 0 {
		i := c.calls
		if i >= len(c.seq) {
			i = len(c.seq) - 1
		}
		credits = c.seq[i]
	}
	c.calls++
	return &player.AgentData{Credits: credits}, nil
}

// newPPCeilHandler builds a minimal tour handler wired for pre-positioning with the
// given ceiling percent and (optional) live-treasury api client, returning the spy
// finder so a test can read its call count.
func newPPCeilHandler(enabled bool, ceilingPct int, api domainPorts.APIClient) (*RunTourCoordinatorHandler, *ppCeilSpyFinder) {
	h := NewRunTourCoordinatorHandler(nil, nil, nil, nil, nil, nil, nil, nil, api)
	finder := &ppCeilSpyFinder{}
	h.SetPrePositioning(
		storageApp.NewInMemoryStorageCoordinator(),
		finder,
		ppCeilNoopMiner{},
		tradingsvc.DepositCandidateConfig{Enabled: enabled, TopN: 5},
		ceilingPct,
	)
	return h, finder
}

func countParked(l *propFloorCapturingLogger) (n int, lastLevel, lastMsg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if strings.Contains(e.message, "Pre-positioning parked") {
			n++
			lastLevel = e.level
			lastMsg = e.message
		}
	}
	return n, lastLevel, lastMsg
}

func ppCmd(container string) *RunTourCoordinatorCommand {
	return &RunTourCoordinatorCommand{ShipSymbol: "TOUR-" + container, PlayerID: 1, ContainerID: container}
}

// A 0/absent capital ceiling parks pre-positioning: no candidates, the funnel is never
// entered (finder untouched), and the dormant state logs exactly ONCE across many
// re-plans — not once per tick.
func TestDepositCandidates_DormantWhenNoCeilingConfigured_ParksAndLogsOnce(t *testing.T) {
	h, finder := newPPCeilHandler(true, 0, nil) // ceilingPct 0 => dormant
	logger := &propFloorCapturingLogger{}
	ctx := propFloorCtx("TOK", logger)
	cmd := ppCmd("ctr-dormant")

	for i := 0; i < 4; i++ {
		if out := h.depositCandidates(ctx, cmd, []string{"X1-HOME"}, 1_000_000); out != nil {
			t.Fatalf("call %d: dormant ceiling must yield NO candidates, got %+v", i, out)
		}
	}

	if c := finder.callCount(); c != 0 {
		t.Fatalf("dormant must NOT enter the deposit funnel (capital binds), finder called %d times", c)
	}
	n, level, msg := countParked(logger)
	if n != 1 {
		t.Fatalf("dormant parked state must log ONCE across 4 re-plans, got %d lines", n)
	}
	if level != "INFO" {
		t.Fatalf("dormant (deliberate config default) is INFO, got %q (%q)", level, msg)
	}
	if !strings.Contains(msg, "dormant") || !strings.Contains(msg, "capital_ceiling_pct") {
		t.Fatalf("dormant line must name the enablement knob, got %q", msg)
	}
}

// A configured ceiling that resolves to 0 because treasury is at/below the reserve
// parks (fail closed) and logs once as INFO (a transient economic condition, mirroring
// the stocker's "capital ceiling is 0" line).
func TestDepositCandidates_ParkedWhenTreasuryAtOrBelowReserve_LogsOnce(t *testing.T) {
	h, finder := newPPCeilHandler(true, 10, &ppCeilAPI{credits: 500_000}) // 500k < 1M reserve
	logger := &propFloorCapturingLogger{}
	ctx := propFloorCtx("TOK", logger)
	cmd := ppCmd("ctr-belowreserve")

	for i := 0; i < 3; i++ {
		if out := h.depositCandidates(ctx, cmd, []string{"X1-HOME"}, 1_000_000); out != nil {
			t.Fatalf("call %d: ceiling 0 must yield NO candidates, got %+v", i, out)
		}
	}
	if c := finder.callCount(); c != 0 {
		t.Fatalf("treasury<=reserve must NOT enter the funnel, finder called %d times", c)
	}
	n, level, msg := countParked(logger)
	if n != 1 {
		t.Fatalf("ceiling-0 state must log ONCE across 3 re-plans, got %d", n)
	}
	if level != "INFO" {
		t.Fatalf("ceiling-0 (treasury<=reserve) is INFO, got %q (%q)", level, msg)
	}
	if !strings.Contains(msg, "capital ceiling 0") || !strings.Contains(msg, "reserve") {
		t.Fatalf("ceiling-0 line must name the reserve cause, got %q", msg)
	}
}

// An unreadable live balance fails CLOSED (RULINGS #4): no candidates, WARNING, once.
func TestDepositCandidates_ParkedWhenBalanceUnreadable_WarnsOnce(t *testing.T) {
	h, finder := newPPCeilHandler(true, 10, &ppCeilAPI{err: true})
	logger := &propFloorCapturingLogger{}
	ctx := propFloorCtx("TOK", logger)
	cmd := ppCmd("ctr-unreadable")

	for i := 0; i < 3; i++ {
		if out := h.depositCandidates(ctx, cmd, []string{"X1-HOME"}, 1_000_000); out != nil {
			t.Fatalf("call %d: unreadable balance must yield NO candidates", i)
		}
	}
	if c := finder.callCount(); c != 0 {
		t.Fatalf("unreadable balance must NOT enter the funnel, finder called %d times", c)
	}
	n, level, msg := countParked(logger)
	if n != 1 {
		t.Fatalf("unreadable-balance state must log ONCE, got %d", n)
	}
	if level != "WARNING" {
		t.Fatalf("unreadable balance (anomaly) is WARNING, got %q (%q)", level, msg)
	}
	if !strings.Contains(msg, "unreadable") {
		t.Fatalf("unreadable line must say so, got %q", msg)
	}
}

// With a configured ceiling and treasury above the reserve, capital is available: the
// funnel IS entered (finder consulted) and NO parked line is emitted.
func TestDepositCandidates_ReachesFunnelWhenCapitalAvailable(t *testing.T) {
	h, finder := newPPCeilHandler(true, 10, &ppCeilAPI{credits: 100_000_000})
	logger := &propFloorCapturingLogger{}
	ctx := propFloorCtx("TOK", logger)
	cmd := ppCmd("ctr-funded")

	// finder returns no warehouse -> BuildDepositCandidates returns empty, but the
	// point is that it WAS reached (capital did not bind).
	_ = h.depositCandidates(ctx, cmd, []string{"X1-HOME"}, 1_000_000)

	if c := finder.callCount(); c != 1 {
		t.Fatalf("capital available must ENTER the funnel exactly once, finder called %d times", c)
	}
	if n, _, msg := countParked(logger); n != 0 {
		t.Fatalf("capital available must emit NO parked line, got %d (%q)", n, msg)
	}
}

// A hull that parks, then gets capital, then parks again logs the parked reason on BOTH
// park episodes (the "or on state change" half): the capital-available pass clears the
// remembered state.
func TestDepositCandidates_ReLogsParkedAfterStateChange(t *testing.T) {
	// Sequential balances: parked (below reserve) -> funded (above) -> parked again.
	api := &ppCeilAPI{seq: []int{500_000, 100_000_000, 500_000}}
	h, finder := newPPCeilHandler(true, 10, api)
	logger := &propFloorCapturingLogger{}
	ctx := propFloorCtx("TOK", logger)
	cmd := ppCmd("ctr-flap")

	for i := 0; i < 3; i++ {
		_ = h.depositCandidates(ctx, cmd, []string{"X1-HOME"}, 1_000_000)
	}

	if c := finder.callCount(); c != 1 {
		t.Fatalf("only the funded pass enters the funnel, finder called %d times", c)
	}
	if n, _, _ := countParked(logger); n != 2 {
		t.Fatalf("park -> funded -> park must re-log the second park (state change), got %d parked lines", n)
	}
}

// De-dup is per container: two hulls each log their own parked line (one hull's log
// never suppresses another's).
func TestDepositCandidates_DedupIsPerContainer(t *testing.T) {
	h, _ := newPPCeilHandler(true, 0, nil)
	logger := &propFloorCapturingLogger{}
	ctx := propFloorCtx("TOK", logger)

	for i := 0; i < 3; i++ {
		_ = h.depositCandidates(ctx, ppCmd("ctr-A"), []string{"X1-HOME"}, 1_000_000)
		_ = h.depositCandidates(ctx, ppCmd("ctr-B"), []string{"X1-HOME"}, 1_000_000)
	}
	if n, _, _ := countParked(logger); n != 2 {
		t.Fatalf("two containers must produce exactly two parked lines, got %d", n)
	}
}

// The deliberate off-switch (Enabled=false) stays fully silent: no candidates, no log,
// no funnel — unchanged from pre-sp-13tl.
func TestDepositCandidates_DisabledIsSilent(t *testing.T) {
	h, finder := newPPCeilHandler(false, 10, &ppCeilAPI{credits: 100_000_000})
	logger := &propFloorCapturingLogger{}
	ctx := propFloorCtx("TOK", logger)

	if out := h.depositCandidates(ctx, ppCmd("ctr-off"), []string{"X1-HOME"}, 1_000_000); out != nil {
		t.Fatalf("disabled must yield no candidates, got %+v", out)
	}
	if c := finder.callCount(); c != 0 {
		t.Fatalf("disabled must not enter the funnel, finder called %d", c)
	}
	if n, _, _ := countParked(logger); n != 0 {
		t.Fatalf("disabled off-switch must be silent, got %d parked lines", n)
	}
}
