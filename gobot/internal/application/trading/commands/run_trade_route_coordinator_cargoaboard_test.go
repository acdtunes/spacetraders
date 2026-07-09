package commands

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// metaCapturingLogger records every structured log line runCircuit emits
// INCLUDING its metadata map, so a test can assert on the structured fields an
// operator would actually see, not just the response's AbortReason. (The
// dockrace test's capturingLogger deliberately drops the metadata; the
// cargo_aboard_exit contract is precisely about that metadata, so this one
// keeps it.) It is the ContainerLogger runCircuit reads back out of the context
// via common.LoggerFromContext.
type metaCapturingLogger struct {
	mu      sync.Mutex
	entries []metaCapturedLog
}

type metaCapturedLog struct {
	level    string
	message  string
	metadata map[string]interface{}
}

func (l *metaCapturingLogger) Log(level, message string, metadata map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, metaCapturedLog{level: level, message: message, metadata: metadata})
}

func (l *metaCapturingLogger) findByAction(action string) *metaCapturedLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.entries {
		if l.entries[i].metadata != nil && l.entries[i].metadata["action"] == action {
			return &l.entries[i]
		}
	}
	return nil
}

// A PRE-SELL circuit exit — the hull has already BOUGHT and is holding cargo it
// could not sell — must emit the structured cargo_aboard_exit record carrying
// good/source/dest/held/reason, not a bare {"error": ...} the container-log
// renderer drops (sp-149h, the sp-ynuf/sp-iqyq class). This drives the sell-leg
// exit: the buy succeeds, the sell is rejected by the market, and the hull is
// left holding the load. The operator must be able to see WHAT is stranded and
// WHERE from the log line alone.
func TestTradeRouteCoordinator_PreSellExit_EmitsStructuredCargoAboardLog(t *testing.T) {
	ship := newResidualHauler(t, "T10", 40, 0) // empty hull: the BUY succeeds, then the SELL fails with cargo aboard
	handler, mediator := newZvHarness(t, ship, 0)
	mediator.failSell = fmt.Errorf("market rejected sell: good not accepted here (4602)")

	logger := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	resp, err := handler.Handle(ctx, &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: zvSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("a pre-sell leg failure must be a clean exit, not an error: %v", err)
	}
	coord, ok := resp.(*RunTradeRouteCoordinatorResponse)
	if !ok || coord == nil {
		t.Fatalf("unexpected response %T", resp)
	}

	// A rejected sell after a successful buy flies zero completed visits.
	if coord.Visits != 0 {
		t.Fatalf("expected 0 visits after the sell was rejected, got %d", coord.Visits)
	}
	// AbortReason still carries the operator-facing prose (unchanged contract).
	if !strings.Contains(coord.AbortReason, "sell") || !strings.Contains(coord.AbortReason, "4602") {
		t.Fatalf("AbortReason must name the failed sell leg and carry the cause, got %q", coord.AbortReason)
	}

	// THE sp-149h contract: the structured cargo-aboard record must be emitted.
	entry := logger.findByAction("cargo_aboard_exit")
	if entry == nil {
		t.Fatal("expected a structured cargo_aboard_exit log after a pre-sell failure with cargo aboard — a bare {\"error\": ...} the renderer drops is exactly the defect sp-149h closes")
	}

	// good/source/dest must pin WHAT is stranded and WHERE.
	if entry.metadata["good"] != zvGood {
		t.Fatalf("cargo_aboard_exit good=%v, want %q", entry.metadata["good"], zvGood)
	}
	if entry.metadata["source"] != zvSrc {
		t.Fatalf("cargo_aboard_exit source=%v, want %q", entry.metadata["source"], zvSrc)
	}
	if entry.metadata["dest"] != zvDst {
		t.Fatalf("cargo_aboard_exit dest=%v, want %q", entry.metadata["dest"], zvDst)
	}
	// held must be a positive int: the whole point is that cargo is stranded aboard.
	held, ok := entry.metadata["held"].(int)
	if !ok || held <= 0 {
		t.Fatalf("cargo_aboard_exit held must be a positive int (units stranded aboard), got %v", entry.metadata["held"])
	}
	// reason must name the specific failed leg and carry its verbatim cause.
	reason, ok := entry.metadata["reason"].(string)
	if !ok || !strings.Contains(reason, "sell") || !strings.Contains(reason, "4602") {
		t.Fatalf("cargo_aboard_exit reason must name the failed sell leg and carry the cause, got %v", entry.metadata["reason"])
	}
}
