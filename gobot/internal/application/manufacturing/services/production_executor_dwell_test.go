package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-npyr: goods_factory coordinators were observed DWELLING 40+ minutes
// holding docked light-hauler claims during a fabrication wait with only
// sparse (every-5th-attempt) logging and no escalation — from the outside
// this reads as a silent stall ("Idle light haulers discovered" fires
// elsewhere every 30s with zero explanation of what the claimed hulls are
// doing). PollForProduction is the TRUE claim-holding site: this test proves
// that once a poll's elapsed wait crosses productionDwellWarnThreshold, the
// dwell reason becomes visible in the logs on every subsequent attempt,
// while the pre-threshold cadence (existing, sparse) is unchanged.

// dwellCapturedLogEntry/dwellCapturingLogger mirror the commands package's
// capturingLogger (run_manufacturing_task_worker_test.go) — no shared helper
// exists across packages, so this is a package-local equivalent for
// `services` (confirmed absent from this package prior to sp-npyr).
type dwellCapturedLogEntry struct {
	level   string
	message string
}

type dwellCapturingLogger struct {
	entries []dwellCapturedLogEntry
}

func (l *dwellCapturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.entries = append(l.entries, dwellCapturedLogEntry{level: level, message: message})
}

func (l *dwellCapturingLogger) entriesWithLevel(level string) []dwellCapturedLogEntry {
	var out []dwellCapturedLogEntry
	for _, e := range l.entries {
		if e.level == level {
			out = append(out, e)
		}
	}
	return out
}

// dwellTestClock self-advances Now() by a fixed step on every call, letting a
// handful of real-fast loop iterations (via pollingIntervals:
// {time.Millisecond}) simulate crossing a multi-minute elapsed threshold in
// milliseconds of real test time. Sleep is a no-op: PollForProduction's
// inter-poll wait uses a real time.NewTimer, not e.clock.Sleep, so the clock
// only needs to drive the elapsed-time calculation.
type dwellTestClock struct {
	current time.Time
	step    time.Duration
}

func (c *dwellTestClock) Now() time.Time {
	now := c.current
	c.current = c.current.Add(c.step)
	return now
}

func (c *dwellTestClock) Sleep(time.Duration) {}

// dwellMarketRepo reports dockRaceGood as NOT YET produced for the first
// notFoundUntil calls to GetMarketData, then produced afterward — modeling a
// factory still fabricating, then finishing. Embeds market.MarketRepository
// so only the one method under test needs to be implemented.
type dwellMarketRepo struct {
	market.MarketRepository
	calls         int
	notFoundUntil int
}

func (r *dwellMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	r.calls++
	if r.calls <= r.notFoundUntil {
		return market.NewMarket(waypointSymbol, nil, time.Now())
	}
	supply := "MODERATE"
	activity := "GROWING"
	good, err := market.NewTradeGood(dockRaceGood, &supply, &activity, 80, 100, 20, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
}

func TestPollForProduction_DwellPastThreshold_EscalatesToWarnEveryAttempt(t *testing.T) {
	clock := &dwellTestClock{current: time.Now(), step: 2 * time.Minute}
	// notFoundUntil=6 crosses the 5-minute threshold partway through the
	// not-found phase (elapsed hits 6m on the 3rd check), leaving both a
	// pre-threshold window (attempts 0-1) and a post-threshold window
	// (attempts 2-5) to assert on before eventual success.
	marketRepo := &dwellMarketRepo{notFoundUntil: 6}
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	executor := NewProductionExecutorWithConfig(
		nil, // mediator: unused — inputsOnly=true skips the purchase path
		nil, // shipRepo: unused for the same reason
		marketRepo,
		nil, // marketLocator: unused by PollForProduction
		clock,
		[]time.Duration{time.Millisecond}, // keep the real inter-poll timer fast
	)

	_, _, err := executor.PollForProduction(
		ctx,
		dockRaceGood,
		dockRaceMarketWP,
		dockRaceShip,
		shared.MustNewPlayerID(1),
		nil,
		true, // inputsOnly
	)
	if err != nil {
		t.Fatalf("expected eventual success once the market reports the good, got error: %v", err)
	}

	warnEntries := logger.entriesWithLevel("WARNING")
	if len(warnEntries) == 0 {
		t.Fatal("expected at least one WARNING log once the dwell crossed productionDwellWarnThreshold, got none")
	}
	for _, e := range warnEntries {
		if !strings.Contains(e.message, dockRaceGood) {
			t.Errorf("dwell WARNING message must name the good being awaited, got: %q", e.message)
		}
		if !strings.Contains(e.message, dockRaceMarketWP) {
			t.Errorf("dwell WARNING message must name the waypoint, got: %q", e.message)
		}
	}

	// Pre-threshold cadence must be unchanged: only the attempt==0 poll logs
	// INFO (attempt 1 is skipped since 1%5 != 0), matching the existing
	// sparse "every 5th attempt" behavior untouched by this fix.
	infoPollEntries := 0
	for _, e := range logger.entriesWithLevel("INFO") {
		if strings.Contains(e.message, "Polling for production completion") {
			infoPollEntries++
		}
	}
	if infoPollEntries != 1 {
		t.Errorf("expected exactly 1 pre-threshold 'Polling for production completion' INFO log (attempt 0 only), got %d", infoPollEntries)
	}
}

func TestPollForProduction_BeforeThreshold_NoWarnEscalation(t *testing.T) {
	// A short wait (never crosses productionDwellWarnThreshold) must produce
	// zero WARNING dwell logs — escalation is specific to genuinely long
	// dwells, not a blanket noise increase on every poll.
	clock := &dwellTestClock{current: time.Now(), step: time.Second}
	marketRepo := &dwellMarketRepo{notFoundUntil: 1}
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	executor := NewProductionExecutorWithConfig(
		nil, nil, marketRepo, nil, clock,
		[]time.Duration{time.Millisecond},
	)

	_, _, err := executor.PollForProduction(
		ctx,
		dockRaceGood,
		dockRaceMarketWP,
		dockRaceShip,
		shared.MustNewPlayerID(1),
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("expected eventual success, got error: %v", err)
	}

	if warnEntries := logger.entriesWithLevel("WARNING"); len(warnEntries) != 0 {
		t.Errorf("expected zero dwell WARNING logs for a short poll, got %d: %+v", len(warnEntries), warnEntries)
	}
}
