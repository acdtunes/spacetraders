package commands

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// idleCaptureLogger records the container log so a test can assert on a line the handler emits
// rather than on a return value it does not have.
type idleCaptureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *idleCaptureLogger) Log(level, message string, _ map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, level+" "+message)
}

func (l *idleCaptureLogger) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// sp-5vv65: NAME THE CAUSAL LINK, ONCE.
//
// The gate sat flat at 85% for six hours. The only evidence was a `(0 tasks promoted to READY)` line
// nobody watches, a 60x drop in log volume, and a roles line reading `want 0D/4F` that looks routine.
// Nothing said the drain was dispatching nothing BECAUSE delivery was paused to zero hulls.

func idleCauseLog(t *testing.T) (*RunConstructionCoordinatorHandler, *idleCaptureLogger, context.Context) {
	t.Helper()
	logs := &idleCaptureLogger{}
	return &RunConstructionCoordinatorHandler{}, logs, common.WithLogger(context.Background(), logs)
}

// THE TRANSITION IS REPORTED, AND NAMES THE CAUSE.
func TestReportDeliveryIdle_NamesWhyTheDrainIsQuiet(t *testing.T) {
	h, logs, ctx := idleCauseLog(t)

	h.reportDeliveryIdleTransition(ctx, true, 0)

	text := logs.text()
	if !strings.Contains(text, "dispatch NOTHING") {
		t.Fatalf("the line must say the drain will dispatch nothing, which is the consequence nobody could infer:\n%s", text)
	}
	if !strings.Contains(text, "PAUSED") {
		t.Fatalf("the line must name the pause as the CAUSE:\n%s", text)
	}
	if !strings.Contains(text, "not a wedged engine") {
		t.Fatalf("the line must pre-empt the wrong conclusion — the engine was healthy the whole six hours and looked dead:\n%s", text)
	}
}

// ONCE, NOT PER TICK. On the incident day the fleet was paused on all 59 ticks of a HEALTHY hour;
// a per-tick line there is 59 identical rows and trains its reader to scroll past the wording that
// mattered during the outage.
func TestReportDeliveryIdle_ReportsOnTransitionNotEveryTick(t *testing.T) {
	h, logs, ctx := idleCauseLog(t)

	for i := 0; i < 59; i++ {
		h.reportDeliveryIdleTransition(ctx, true, 0)
	}

	if got := strings.Count(logs.text(), "dispatch NOTHING"); got != 1 {
		t.Fatalf("emitted the idle line %d times across 59 paused ticks, want exactly 1. A per-tick line reproduces the noise this exists to cut through", got)
	}
}

// AND IT REARMS: a pause that lifts and returns is reported again, or the second outage is silent.
func TestReportDeliveryIdle_ReportsAgainAfterThePauseLifts(t *testing.T) {
	h, logs, ctx := idleCauseLog(t)

	h.reportDeliveryIdleTransition(ctx, true, 0)  // outage 1
	h.reportDeliveryIdleTransition(ctx, false, 2) // recovery
	h.reportDeliveryIdleTransition(ctx, true, 0)  // outage 2

	text := logs.text()
	if got := strings.Count(text, "dispatch NOTHING"); got != 2 {
		t.Fatalf("reported %d idle transitions across two separate outages, want 2 — a latched flag would silence the second", got)
	}
	if !strings.Contains(text, "no longer paused") {
		t.Fatalf("the recovery must be reported too, or the reader cannot tell a cleared pause from an ongoing one:\n%s", text)
	}
}

// A PAUSE THAT STILL WANTS DELIVERY HULLS IS NOT THE DRAIN-IDLE SHAPE. Only "paused AND zero hulls
// wanted" stops dispatch, and reporting the broader case would fire on pauses that dispatch fine.
func TestReportDeliveryIdle_StaysSilentWhenDeliveryHullsAreStillWanted(t *testing.T) {
	h, logs, ctx := idleCauseLog(t)

	h.reportDeliveryIdleTransition(ctx, true, 2) // paused, but 2 delivery hulls still wanted
	h.reportDeliveryIdleTransition(ctx, false, 0)

	if strings.Contains(logs.text(), "dispatch NOTHING") {
		t.Fatalf("reported a drain-idle cause when delivery hulls were still wanted:\n%s", logs.text())
	}
}
