package commands

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests cover the stale-launch-list homing defect (sp-cmwc): the between-legs
// homing gate must key off the LIVE dedicated_fleet tag, not the immutable
// --dedicated-ships launch snapshot, so a hull added via `fleet add --operation
// contract` after launch homes to a standby station between legs instead of being
// market-balanced like a general-pool hull. Shares the restart-resurrection root
// cause (sp-86vb): the coordinator must trust the live tag over the launch snapshot.

// errFindAllRepo fails FindAllByPlayer so the resolver's fallback-to-launch-list
// branch (never worse than the pre-fix behavior) can be exercised.
type errFindAllRepo struct {
	navigation.ShipRepository
	err error
}

func (r *errFindAllRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return nil, r.err
}

// TestHomingGate_LiveAddedShip_RecognizedAsDedicated is the primary sp-cmwc case: a
// hull `fleet add`ed after launch (live tag "contract", absent from the launch list)
// must be recognized as dedicated by the homing gate — so it homes between legs —
// while the stale launch list would have missed it and market-balanced it.
func TestHomingGate_LiveAddedShip_RecognizedAsDedicated(t *testing.T) {
	added := newHaulerShip(t, "TORWIND-9", "contract") // fleet-add'd after launch: tag only
	general := newHaulerShip(t, "TORWIND-2", "")       // general pool
	repo := &inMemoryFleetRepo{ships: map[string]*navigation.Ship{
		"TORWIND-9": added,
		"TORWIND-2": general,
	}}
	logger := &completionCapturingLogger{}
	launchList := []string{} // the ship was NEVER in the --dedicated-ships launch seed

	members := resolveDedicatedMembersForHoming(context.Background(), logger, repo,
		shared.MustNewPlayerID(2), "contract", launchList)

	// THE FIX: live membership recognizes the fleet-add'd hull, so the homing gate
	// homes it to a standby station between legs.
	if !isDedicatedShip("TORWIND-9", members) {
		t.Fatalf("live-added contract member must be recognized as dedicated (homed between legs), got members %v", members)
	}
	// THE BUG IT FIXES: the frozen launch list does not contain the live-added hull,
	// so the old gate market-balanced it like a general-pool ship.
	if isDedicatedShip("TORWIND-9", launchList) {
		t.Fatalf("guard: the launch list must NOT contain the live-added ship — that staleness is the defect")
	}
	// A genuine general-pool hull is still not dedicated: it keeps normal market
	// balancing, unchanged.
	if isDedicatedShip("TORWIND-2", members) {
		t.Fatalf("a general-pool hull must not be treated as dedicated, got members %v", members)
	}
}

// TestHomingGate_LiveRemovedShip_NoLongerDedicated is the symmetric sp-cmwc case: a
// hull `fleet remove`d (tag cleared) must NOT be treated as dedicated any longer,
// even if it is still named in the immutable launch list.
func TestHomingGate_LiveRemovedShip_NoLongerDedicated(t *testing.T) {
	removed := newHaulerShip(t, "TORWIND-7", "") // was seeded, then `fleet remove`d → tag ""
	repo := &inMemoryFleetRepo{ships: map[string]*navigation.Ship{"TORWIND-7": removed}}
	logger := &completionCapturingLogger{}
	launchList := []string{"TORWIND-7"} // stale: still on the launch --dedicated-ships list

	members := resolveDedicatedMembersForHoming(context.Background(), logger, repo,
		shared.MustNewPlayerID(2), "contract", launchList)

	if isDedicatedShip("TORWIND-7", members) {
		t.Fatalf("a live-removed hull must not be treated as dedicated by the homing gate, got members %v", members)
	}
}

// TestHomingGate_MembershipReadError_FallsBackToLaunchList proves the resolver never
// regresses below the pre-fix behavior: if the live membership read fails, it falls
// back to the launch list and warns, rather than losing all dedication data.
func TestHomingGate_MembershipReadError_FallsBackToLaunchList(t *testing.T) {
	repo := &errFindAllRepo{err: fmt.Errorf("db unavailable")}
	logger := &completionCapturingLogger{}
	launchList := []string{"TORWIND-4", "TORWIND-5"}

	members := resolveDedicatedMembersForHoming(context.Background(), logger, repo,
		shared.MustNewPlayerID(2), "contract", launchList)

	if !reflect.DeepEqual(members, launchList) {
		t.Fatalf("on a membership read error the resolver must fall back to the launch list, got %v", members)
	}
	foundWarning := false
	for _, entry := range logger.entries {
		if entry.level == "WARNING" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("expected a WARNING when the live membership read fails, got entries: %+v", logger.entries)
	}
}
