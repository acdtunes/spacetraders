package grpc

// sp-hoc6: sustain the gate's source EXPORT-factory export supply by auto-launching a STANDING
// InputsOnly goods_factory feeder for each configured gate source material, ALONGSIDE the
// construction drain. THE MODEL (Admiral): we always BUY the gate output (e.g. FAB_MATS@F48,
// ADVANCED_CIRCUITRY@D42) and haul it to the gate; "producing" means FEEDING each source factory its
// raw-material INPUTS so it keeps producing and its export supply/price stay healthy — so our buying
// stays under the buy-ceiling (sp-layd). Buying alone depletes export supply → the bid explodes → the
// buy-ceiling trips → the gate-fill stalls.
//
// This is ORCHESTRATION over existing capabilities, not new sourcing logic: the feed-inputs-then-leave
// -output machinery already ships as goods_factory with InputsOnly=true (sp-q02m) — it feeds the
// factory its imports and LEAVES the fabricated output in export stock for a SEPARATE buyer. The gap
// this closes is purely launch-wiring: nothing ran an InputsOnly feeder for the gate's buy-direct
// source factories, so the drain bought the output with ZERO feeding. We launch the existing op; the
// drain stays the SOLE buyer + hauler of the output.
//
// The buy-vs-feed launch DECISION (planGateSourceFeeders) is a pure function — no DB, no goroutines —
// so it is fully unit-tested. The impure ensureGateSourceFeeders wraps it: read which InputsOnly
// feeders already run (idempotency), resolve the home system only when a launch actually needs it, and
// forward each launch straight to StartGoodsFactory.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// goodsFactoryContainerType is the container/command type StartGoodsFactory persists (an outlier from
// the uppercase ContainerType constants — the goods factory uses this lowercase string, matched by
// ListByStatusSimple filters and the captain factory-income detectors).
const goodsFactoryContainerType = "goods_factory_coordinator"

// gateSourceFeederLaunch is a resolved feeder launch spec — exactly the arguments a StartGoodsFactory
// feeder call takes. InputsOnly is always true (feed inputs, leave output in export stock for the
// drain — sp-q02m) and Iterations always -1 (a standing/infinite feeder that survives restart via
// RecoverRunningContainers), so the drain remains the sole output buyer/hauler.
type gateSourceFeederLaunch struct {
	Good       string
	System     string
	InputsOnly bool
	Iterations int
}

// planGateSourceFeeders is the pure buy-vs-feed launch decision. Given the configured feeder set, the
// resolved home system, and the goods that ALREADY have a running InputsOnly feeder, it returns the
// feeders to launch now — each as an InputsOnly, infinite (-1) goods_factory.
//
//   - Empty/nil config → no launches: the mechanism never hardcodes a material (RULINGS #5).
//   - A good with a running InputsOnly feeder is skipped (idempotent: RecoverRunningContainers
//     re-adopts persisted feeders on restart, so this never double-launches — RULINGS #2).
//   - An empty System resolves to homeSystem (single-system, RULINGS #14); an explicit System wins.
//   - A blank Good, or an empty-System feeder with no resolvable home system, is skipped rather than
//     launched with a target that cannot find its export market (fail-safe).
//   - sp-vh1s: under the unified gate-fill toggle the standing InputsOnly feeders are DELETED — feeding
//     is now INHERENT in the gate run's recursive tree (it buys the output AND feeds the source factory
//     as one op), so the separate sidecar feeder that collided with it (§4 bug) never launches. Returns
//     nothing regardless of config, so an ON fleet runs no feeders; OFF is byte-identical.
func planGateSourceFeeders(configured []config.GateSourceFeeder, homeSystem string, runningFeederGoods map[string]bool, unifiedGateFill bool) []gateSourceFeederLaunch {
	if unifiedGateFill {
		return nil
	}
	var launches []gateSourceFeederLaunch
	for _, f := range configured {
		if f.Good == "" || runningFeederGoods[f.Good] {
			continue
		}
		system := f.System
		if system == "" {
			system = homeSystem
		}
		if system == "" {
			continue // unresolvable system — do not launch a feeder that cannot locate its factory
		}
		launches = append(launches, gateSourceFeederLaunch{
			Good:       f.Good,
			System:     system,
			InputsOnly: true,
			Iterations: -1,
		})
	}
	return launches
}

// anyFeederMissing reports whether some configured (non-blank) good has no running InputsOnly feeder
// yet. It gates the impure home-system resolution: a warm restart with every feeder re-adopted returns
// false, so the boot pass launches nothing and pays for no ship read.
func anyFeederMissing(configured []config.GateSourceFeeder, runningFeederGoods map[string]bool) bool {
	for _, f := range configured {
		if f.Good != "" && !runningFeederGoods[f.Good] {
			return true
		}
	}
	return false
}

// parseFeederConfig reads a persisted goods_factory container's target good and inputs_only flag from
// its Config JSON (StartGoodsFactory persists both into metadata). Returns ("", false) on absent or
// unparseable config, so a malformed row is simply not counted (never a panic).
func parseFeederConfig(configJSON string) (good string, inputsOnly bool) {
	var fc struct {
		TargetGood string `json:"target_good"`
		InputsOnly bool   `json:"inputs_only"`
	}
	if err := json.Unmarshal([]byte(configJSON), &fc); err != nil {
		return "", false
	}
	return fc.TargetGood, fc.InputsOnly
}

// ensureGateSourceFeeders launches a standing InputsOnly goods_factory feeder for each configured gate
// source material that is not already fed, ALONGSIDE the construction drain (sp-hoc6). Idempotent and
// safe to call every boot: goods_factory feeders are re-adopted by RecoverRunningContainers on
// restart, and this pass skips any good already fed, so a restart never double-launches (RULINGS #2 —
// the same resilience shape as the construction drain's boot-standing EnsureRunning). Every failure is
// logged and non-fatal — feeder launch must never block daemon startup.
func (s *DaemonServer) ensureGateSourceFeeders(ctx context.Context, playerID int) {
	// sp-vh1s: under the unified gate-fill toggle the standing InputsOnly feeders are retired outright —
	// feeding is inherent in the gate run — so skip the whole pass (incl. every DB/ship read). OFF keeps
	// the sp-hoc6 feeder launch exactly as before.
	if s.manufacturingConfig.UnifiedGateFill {
		return
	}
	configured := s.bootstrapConfig.GateSourceFeeders
	if len(configured) == 0 {
		return
	}

	running, err := s.runningInputsOnlyFeederGoods(ctx, playerID)
	if err != nil {
		fmt.Printf("Warning: gate-source-feeder launch skipped — cannot read running feeders: %v\n", err)
		return
	}

	// Nothing to launch (warm restart: recovery re-adopted every feeder) → skip, incl. the ship read.
	if !anyFeederMissing(configured, running) {
		return
	}

	// Resolve the home system for feeders whose System is left to default (single-system, RULINGS #14).
	homeSystem := s.deriveHomeSystemFromShips(ctx, playerID)

	for _, l := range planGateSourceFeeders(configured, homeSystem, running, s.manufacturingConfig.UnifiedGateFill) {
		if _, err := s.StartGoodsFactory(ctx, l.Good, l.System, playerID, l.Iterations, l.InputsOnly); err != nil {
			fmt.Printf("Warning: failed to launch gate-source InputsOnly feeder for %s in %s: %v\n", l.Good, l.System, err)
		}
	}
}

// runningInputsOnlyFeederGoods returns the set of goods that already have a RUNNING or PENDING
// InputsOnly goods_factory feeder for the player. Only InputsOnly feeders count — a harvesting factory
// for the same good is NOT one of ours (it would compete with the drain as a buyer), so it must not
// suppress the feeder launch.
func (s *DaemonServer) runningInputsOnlyFeederGoods(ctx context.Context, playerID int) (map[string]bool, error) {
	goodsRunning := make(map[string]bool)
	for _, st := range []container.ContainerStatus{container.ContainerStatusRunning, container.ContainerStatusPending} {
		models, err := s.containerRepo.ListByStatus(ctx, st, &playerID)
		if err != nil {
			return nil, err
		}
		for _, m := range models {
			if m.ContainerType != goodsFactoryContainerType {
				continue
			}
			good, inputsOnly := parseFeederConfig(m.Config)
			if inputsOnly && good != "" {
				goodsRunning[good] = true
			}
		}
	}
	return goodsRunning, nil
}

// deriveHomeSystemFromShips resolves the player's home system from its ships' current location — the
// same source the bootstrap observer uses (a ship's waypoint → its system). Best-effort: any miss
// returns "", and planGateSourceFeeders then skips default-system feeders rather than launching a
// factory that cannot locate its export market.
func (s *DaemonServer) deriveHomeSystemFromShips(ctx context.Context, playerID int) string {
	if s.shipRepo == nil {
		return ""
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return ""
	}
	ships, err := s.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return ""
	}
	for _, sh := range ships {
		if loc := sh.CurrentLocation(); loc != nil && loc.Symbol != "" {
			return shared.ExtractSystemSymbol(loc.Symbol)
		}
	}
	return ""
}
