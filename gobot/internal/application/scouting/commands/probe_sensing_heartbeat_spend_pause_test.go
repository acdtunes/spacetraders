package commands

// The cycle line has to SAY the switch is off.
//
// Nobody watches this loop run — the heartbeat IS the standing sensor — and the
// whole cost of sp-com1h was a line that looked healthy while the switch it named
// did not cover the spending. It read `bought 6 reused 0 queued 5` with
// expansion_enabled=2, over and over, and there was nothing in it to contradict.
//
// So the fix is not finished when the drain stops buying. `bought 0` alone is
// ambiguous — it is equally a probe cap, a buy floor, an empty queue, or a dead
// engine — and each of those sends an operator somewhere different. The line must
// name the operator's own switch, in words, or the next money hunt starts at the
// treasury again.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// messageLogger keeps the rendered MESSAGE, which capturingLogger deliberately
// does not: it records payloads only, and the payload is not what an operator
// reads at 4am.
type messageLogger struct {
	mu       sync.Mutex
	messages []string
	fields   []map[string]interface{}
}

func (l *messageLogger) Log(_ string, msg string, fields map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, msg)
	l.fields = append(l.fields, fields)
}

// cycleLine renders one heartbeat for the given buy report and returns the message
// and its structured payload.
func cycleLine(t *testing.T, rep parkedsensing.BuyReport) (string, map[string]interface{}) {
	t.Helper()
	log := &messageLogger{}
	h := &RunProbeSensingCoordinatorHandler{}
	h.heartbeat(common.WithLogger(context.Background(), log),
		&RunProbeSensingCoordinatorCommand{ContainerID: "probe_sensing_coordinator-player-1-24f32043"},
		sensingConfig{ProbeCap: 800},
		heartbeat{buy: rep})

	if len(log.messages) != 1 {
		t.Fatalf("want exactly one cycle line, got %d", len(log.messages))
	}
	return log.messages[0], log.fields[0]
}

// THE REGRESSION PROOF for the reporting half. A paused tick reports bought 0 AND
// names the switch that stopped it.
func TestHeartbeat_PausedBuyQueueNamesTheExpansionSwitch(t *testing.T) {
	msg, fields := cycleLine(t, parkedsensing.BuyReport{SpendingPaused: true, Reused: 2, Attempts: 2})

	if !strings.Contains(msg, "bought 0 ") {
		t.Fatalf("cycle line does not report bought 0: %q", msg)
	}
	if !strings.Contains(msg, "expansion_enabled") {
		t.Fatalf("cycle line does not name the switch that stopped the buying: %q\n"+
			"bought 0 on its own is equally a probe cap, a buy floor, an empty queue or a dead engine, "+
			"and each of those sends an operator somewhere different (sp-com1h)", msg)
	}
	if paused, ok := fields["buy_spending_paused"].(bool); !ok || !paused {
		t.Fatalf("payload does not carry buy_spending_paused=true: %v — the switch state has to be "+
			"queryable beside buy_bought, or 'off and still spending' stays a manual correlation across "+
			"two engines' log lines", fields["buy_spending_paused"])
	}
	// The free half still ran, and the line still shows it: a pause is not a stall.
	if !strings.Contains(msg, "reused 2") {
		t.Fatalf("cycle line hid the free work the paused tick did: %q", msg)
	}
}

// The switch outranks the ceilings in the one-reason slot, and that ordering is the
// point rather than a tidy-up. "Held at the buy floor" is the fleet declining to
// afford a probe it still wants and starts a treasury investigation; the switch is a
// decision the operator already made. Reporting the wrong one costs an hour.
func TestHeartbeat_SwitchOutranksTheMoneyCeilingsInTheHeldReason(t *testing.T) {
	msg, _ := cycleLine(t, parkedsensing.BuyReport{SpendingPaused: true, CapHeld: true, FloorHeld: true})

	if !strings.Contains(msg, "expansion_enabled") {
		t.Fatalf("cycle line reported a money ceiling over the operator's own switch: %q", msg)
	}
	if strings.Contains(msg, "probe cap") || strings.Contains(msg, "buy floor") {
		t.Fatalf("cycle line reported a ceiling a paused tick never evaluated: %q", msg)
	}
}

// And an unpaused tick's line is untouched: the switch text appears only when the
// switch is off, so the ordinary cycle line an operator has learned to read is the
// same one it was.
func TestHeartbeat_UnpausedCycleLineNeverMentionsTheSwitch(t *testing.T) {
	msg, fields := cycleLine(t, parkedsensing.BuyReport{Bought: 6, Queued: 5, Attempts: 6})

	if strings.Contains(msg, "expansion_enabled") {
		t.Fatalf("a spending tick's line mentions the switch: %q", msg)
	}
	if paused, _ := fields["buy_spending_paused"].(bool); paused {
		t.Fatalf("payload claims spending is paused on a tick that bought 6")
	}
	if !strings.Contains(msg, "bought 6") {
		t.Fatalf("cycle line lost its bought count: %q", msg)
	}
}
