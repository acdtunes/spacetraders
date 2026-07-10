package commands

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipAssignment "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/assignment"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reconcileStubMediator records every AssignShipFleetCommand sent through it
// and returns a canned per-symbol error, so tests can assert exactly which
// ships reconciliation tried to dedicate - without a real handler stack.
// Reconciliation routes through the mediator (sp-l7h2), not the repository:
// idempotence (skip the DB write when the tag is unchanged) now lives inside
// ShipRepository.AssignFleet and is covered by the repository's own tests.
type reconcileStubMediator struct {
	sendErr map[string]error                         // ship symbol -> error to return, if any
	sent    []*shipAssignment.AssignShipFleetCommand // commands received, in order
}

func (m *reconcileStubMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*shipAssignment.AssignShipFleetCommand)
	if !ok {
		return nil, fmt.Errorf("unexpected request type %T", request)
	}
	m.sent = append(m.sent, cmd)
	if err, ok := m.sendErr[cmd.ShipSymbol]; ok {
		return nil, err
	}
	return &shipAssignment.AssignShipFleetResponse{ShipSymbol: cmd.ShipSymbol, Fleet: cmd.Fleet}, nil
}

func (m *reconcileStubMediator) Register(reflect.Type, common.RequestHandler) error { return nil }

func (m *reconcileStubMediator) RegisterMiddleware(common.Middleware) {}

// Every symbol on the operator's --dedicated-ships list must be routed
// through AssignShipFleetCommand - the single write path for the dedication
// tag (sp-l7h2) - into the named fleet, so the claim-filter in
// FindIdleLightHaulers and the atomic guard in ClaimShip take effect.
func TestReconcileDedicatedFleet_SendsAssignCommandPerConfiguredShip(t *testing.T) {
	med := &reconcileStubMediator{}
	logger := &completionCapturingLogger{}

	reconcileDedicatedFleet(context.Background(), logger, med, shared.MustNewPlayerID(7), []string{"TORWIND-4", "TORWIND-5"}, "contract", "contract-coordinator-reconcile:test")

	if len(med.sent) != 2 {
		t.Fatalf("expected exactly 2 assign commands, got %d: %+v", len(med.sent), med.sent)
	}
	for i, wantSymbol := range []string{"TORWIND-4", "TORWIND-5"} {
		cmd := med.sent[i]
		if cmd.ShipSymbol != wantSymbol {
			t.Fatalf("command %d: expected ship %s, got %s", i, wantSymbol, cmd.ShipSymbol)
		}
		if cmd.Fleet != "contract" {
			t.Fatalf("command %d: expected fleet %q, got %q", i, "contract", cmd.Fleet)
		}
		if cmd.PlayerID == nil || *cmd.PlayerID != 7 {
			t.Fatalf("command %d: expected player ID 7, got %v", i, cmd.PlayerID)
		}
		// Reconciliation is the AUTOMATED path (sp-r6f1): it must send Manual:
		// false so the handler BLOCKS an ineligible 0-cargo hull, and carry the
		// assigner identity so a mispin names its culprit.
		if cmd.Manual {
			t.Fatalf("command %d: reconcile must be the automated path (Manual=false), got Manual=true", i)
		}
		if cmd.Assigner != "contract-coordinator-reconcile:test" {
			t.Fatalf("command %d: expected assigner identity threaded through, got %q", i, cmd.Assigner)
		}
	}
}

// A failing assignment (e.g. a ship sold or renamed since the operator's
// --dedicated-ships flag was last updated) must log a warning and continue
// reconciling the remaining ships, not abort the whole pass.
func TestReconcileDedicatedFleet_CommandFailure_LogsWarningAndContinues(t *testing.T) {
	med := &reconcileStubMediator{sendErr: map[string]error{
		"TORWIND-GONE": fmt.Errorf("ship TORWIND-GONE not found for player 1"),
	}}
	logger := &completionCapturingLogger{}

	reconcileDedicatedFleet(context.Background(), logger, med, shared.MustNewPlayerID(1), []string{"TORWIND-GONE", "TORWIND-5"}, "contract", "contract-coordinator-reconcile:test")

	if len(med.sent) != 2 {
		t.Fatalf("expected the pass to continue past the failure and send both commands, got %d: %+v", len(med.sent), med.sent)
	}
	if med.sent[1].ShipSymbol != "TORWIND-5" {
		t.Fatalf("expected the known ship to still be reconciled despite the failure, got %s", med.sent[1].ShipSymbol)
	}
	foundWarning := false
	for _, entry := range logger.entries {
		if entry.level == "WARNING" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("expected a WARNING log for the failed assignment, got entries: %+v", logger.entries)
	}
}

// An empty --dedicated-ships list (the default, no dedicated fleet
// configured) must not dispatch any command or log anything at all.
func TestReconcileDedicatedFleet_EmptyList_NoOp(t *testing.T) {
	med := &reconcileStubMediator{}
	logger := &completionCapturingLogger{}

	reconcileDedicatedFleet(context.Background(), logger, med, shared.MustNewPlayerID(1), nil, "contract", "contract-coordinator-reconcile:test")

	if len(med.sent) != 0 {
		t.Fatalf("expected no assign commands for an empty dedicated-ships list, got %+v", med.sent)
	}
	if len(logger.entries) != 0 {
		t.Fatalf("expected no log entries for an empty dedicated-ships list, got %+v", logger.entries)
	}
}

// A symbol present on the operator's --dedicated-ships list is dedicated -
// this decides whether the "previous ship" hook homes a ship instead of
// balancing it to a market (sp-snmb).
func TestIsDedicatedShip_SymbolInList_ReturnsTrue(t *testing.T) {
	if !isDedicatedShip("TORWIND-4", []string{"TORWIND-4", "TORWIND-5"}) {
		t.Fatalf("expected TORWIND-4 to be reported as dedicated")
	}
}

// A symbol absent from the list is not dedicated - it must get the normal
// market-balancing treatment.
func TestIsDedicatedShip_SymbolNotInList_ReturnsFalse(t *testing.T) {
	if isDedicatedShip("TORWIND-9", []string{"TORWIND-4", "TORWIND-5"}) {
		t.Fatalf("expected TORWIND-9 to be reported as not dedicated")
	}
}

// No configured dedicated ships at all means every ship gets the normal
// market-balancing treatment.
func TestIsDedicatedShip_EmptyList_ReturnsFalse(t *testing.T) {
	if isDedicatedShip("TORWIND-4", nil) {
		t.Fatalf("expected no ship to be reported as dedicated with an empty list")
	}
}
