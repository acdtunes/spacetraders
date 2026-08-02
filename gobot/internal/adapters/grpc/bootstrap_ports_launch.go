package grpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// bootstrapFrigateRetirer clears the command frigate's contract dedication (a fleet unassign).
type bootstrapFrigateRetirer struct{ shipRepo navigation.ShipRepository }

// RetireFromContract clears the frigate's dedicated-fleet tag (fleet unassign = AssignFleet with ""),
// removing it from the contract coordinator's dedicated pool. Idempotent at the repo (a clear on an
// untagged hull is a no-op); the reconciler already guards on the observation.
func (r *bootstrapFrigateRetirer) RetireFromContract(ctx context.Context, playerID int, shipSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return r.shipRepo.AssignFleet(ctx, shipSymbol, "", pid)
}

// DedicateAsPurchaser tags the frigate dedicated_fleet="purchasing" at the first-hauler pivot,
// reserving it as the EXCLUSIVE, protected buy ship. Reuses the single fleet-assign write path
// (shipRepo.AssignFleet); the contract-op selection paths skip a purchasing-dedicated hull like any
// foreign dedication (RULINGS #7), so it is never re-drafted while it stands by between buys.
func (r *bootstrapFrigateRetirer) DedicateAsPurchaser(ctx context.Context, playerID int, shipSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return r.shipRepo.AssignFleet(ctx, shipSymbol, navigation.PurchasingFleet, pid)
}

// bootstrapContractRunner launches the contract fleet coordinator.
type bootstrapContractRunner struct{ server *DaemonServer }

// StartBatchContract launches the contract fleet coordinator (dynamic ship discovery — empty slices).
// It re-checks the container repo first (defense in depth beyond the observation's BatchContractRunning
// guard) because ContractFleetCoordinator is not itself idempotent — so a stale observation can never
// spawn a duplicate coordinator.
func (r *bootstrapContractRunner) StartBatchContract(ctx context.Context, playerID int) error {
	running, err := contractFleetCoordinatorRunning(ctx, r.server.containerRepo, playerID)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	_, err = r.server.ContractFleetCoordinator(ctx, nil, playerID, nil, nil)
	return err
}

// bootstrapFrigateContractLoop drives the pre-hauler sole-earner contract loop on the command frigate.
type bootstrapFrigateContractLoop struct{ server *DaemonServer }

// StartLoop puts the command frigate on a continuous single-hull contract loop
// (server.BatchContractWorkflow with iterations=-1 — the sp-ehg9 batch-contract --loop). The daemon's
// per-player single-CONTRACT_WORKFLOW guard (CreateIfNoActiveWorker) makes a duplicate start a benign
// no-op, so an "already running" error is swallowed as success — the reconciler already gates on the
// obs.FrigateContractLoopRunning earner-signal, and this only trips on the rare observation-lag race.
// The loop CLAIMS the frigate via the container runner (IsAssigned), NOT the "contract" fleet tag, so it
// never collides with the frigate-retire (which clears that tag) and the coordinator can never
// double-claim the frigate while the loop holds it (RULINGS #7).
func (r *bootstrapFrigateContractLoop) StartLoop(ctx context.Context, playerID int, frigateSymbol string) error {
	if _, err := r.server.BatchContractWorkflow(ctx, frigateSymbol, playerID, -1); err != nil {
		if strings.Contains(err.Error(), "already running") {
			return nil
		}
		return err
	}
	return nil
}

// StopLoop stops the command frigate's continuous contract-loop container (first-hauler pivot):
// it finds the frigate's CONTRACT_WORKFLOW (iterations=-1) container and StopContainer's it, which
// gracefully cancels the loop goroutine and RELEASES the frigate's work-claim so it goes idle to serve
// as the purchaser. Idempotent: no loop found ⇒ nil (the frigate is already free). The reconciler gates
// the pivot on obs.FrigateContractLoopRunning, so StopLoop is invoked only when a loop is observed.
func (r *bootstrapFrigateContractLoop) StopLoop(ctx context.Context, playerID int, frigateSymbol string) error {
	id, err := findFrigateContractLoopID(ctx, r.server.containerRepo, playerID, frigateSymbol)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	return r.server.StopContainer(id)
}

// bootstrapScoutPostDeclarer declares the home coverage target; the standing scout-post
// coordinator mans it by claiming an idle probe.
type bootstrapScoutPostDeclarer struct{ server *DaemonServer }

// DeclareHomeScoutPost ensures a STANDING scout post exists for the home system so the boot-standing
// scout-post coordinator has a coverage target to man, and stamps its permanent manning
// FLOOR = minHulls (probeTarget). It assigns/dedicates NO probe; the coordinator claims an
// idle one. hulls=1: one probe slot initially; the freshsizer resizes the budget once the system
// enters the scanned census (but never below the floor).
//
// The post ROW is declared ONCE (re-Upsert every tick would churn the coordinator's manning and
// the freshsizer's Hulls resize). The floor is stamped through the NARROW min_hulls-only seam
// (UpdateScoutPostMinHulls) — a DISJOINT column from hulls, so it disturbs neither writer — and is
// idempotent: it fires once on a fresh or pre-floor post, then the equality guard makes it a no-op,
// so a mid-era deploy also gets the floor without per-tick writes.
func (s *bootstrapScoutPostDeclarer) DeclareHomeScoutPost(ctx context.Context, playerID int, system string, minHulls int) error {
	existing, err := s.server.ListScoutPosts(ctx, playerID)
	if err != nil {
		return fmt.Errorf("list scout posts: %w", err)
	}
	var home *domainScouting.ScoutPost
	for _, p := range existing {
		if p.SystemSymbol == system {
			home = p
			break
		}
	}
	if home == nil {
		if _, err := s.server.AddScoutPost(ctx, playerID, system, bootstrapHomeScoutPostFreshness, domainScouting.PostKindStanding, 1); err != nil {
			return err
		}
	}
	// Stamp the permanent home floor iff it is not already the desired value — leaving the
	// coordinator's/freshsizer's live state untouched in the steady state.
	if minHulls > 0 && (home == nil || home.MinHulls != minHulls) {
		return s.server.UpdateScoutPostMinHulls(ctx, playerID, system, minHulls)
	}
	return nil
}
