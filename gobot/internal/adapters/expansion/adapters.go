// Package expansion holds the infrastructure adapters that back the frontier
// expansion coordinator's optional ports: the live-treasury reader, the
// probe price-and-buy port over the existing purchase_ship machinery, and the
// gate-graph/market-data expansion scanner. The coordinator's decision logic lives in
// internal/application/expansion/commands; these adapters are the seams the daemon
// wires it to at composition time (main.go).
package expansion

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// probeShipType is the SpaceTraders purchase type for a scout/satellite hull, matching
// the coordinator's own constant.
const probeShipType = "SHIP_PROBE"

const (
	// probeBuyClaimOperation is the fleet label the buyer-relay journey claim records on
	// ClaimShip. The buyer is always an UNDEDICATED hull (eligibleBuyers / resolveInPlaceBuy filter
	// DedicatedFleet != ""), so ClaimShip's dedication guard is a no-op for it; the label is for
	// observability of who holds the hull.
	probeBuyClaimOperation = "probe_buy"

	// defaultProbeBuyClaimOwner owns the journey claim when the caller passes no
	// ClaimOwnerContainerID, so the exclusive single-writer claim (RULINGS #3) is NEVER silently
	// skipped. The daemon's boot-time ReleaseAllActive frees a claim under ANY owner, so restart
	// cleanup (RULINGS #2) never depends on this value.
	defaultProbeBuyClaimOwner = "probe_buy_relay"

	// probeBuyClaimReleaseReason labels the claim release for the audit trail.
	probeBuyClaimReleaseReason = "probe_buy_relay_complete"
)

// ---- TreasuryReader --------------------------------------------------------

// TreasuryReader reads the player's live treasury from the API for the 25% money guard.
// It resolves the player token from ctx (the daemon container runner injects it), so a
// missing token or an API error surfaces as an error the coordinator fails closed on.
type TreasuryReader struct {
	apiClient domainPorts.APIClient
}

// NewTreasuryReader wires the live-treasury reader.
func NewTreasuryReader(apiClient domainPorts.APIClient) *TreasuryReader {
	return &TreasuryReader{apiClient: apiClient}
}

// LiveCredits returns the player's current treasury balance.
func (r *TreasuryReader) LiveCredits(ctx context.Context, playerID shared.PlayerID) (int, error) {
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("player token unresolved: %w", err)
	}
	agent, err := r.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, err
	}
	return agent.Credits, nil
}

// ---- ProbePurchaser --------------------------------------------------------

// probeYardFinder is the narrow slice of the ReachableYardFinder the demand-proximal
// selection needs: the scout-scanned probe-selling yards near a target system, ranked
// hops-then-price off the persisted gate graph (a pure store read — no API). Satisfied by
// *shipyardQueries.ReachableYardFinder. Nil-safe: a nil finder disables target-aware selection,
// leaving every buy on the home-yard in-place path.
type probeYardFinder interface {
	NearestYardsSelling(ctx context.Context, playerID int, shipTypes []string, fromSystems []string) ([]shipyardQueries.YardCandidate, error)
}

// estImpactPerBuyCredits and impactDecayWindow are the sp-4m4ve Phase 3 (§2D) per-yard
// recent-buy price-impact term's fixed rate and lookback — graduated UNCONDITIONAL (no flag,
// no off path). A candidate yard's effective SELECTION cost is inflated by
// estImpactPerBuyCredits credits for every recent probe PURCHASE_SHIP transaction this player
// made at that yard's waypoint within impactDecayWindow — derived fresh from the PERSISTED
// ledger on every call (restart-safe, RULINGS #2: never an in-memory per-yard counter a daemon
// restart would lose). This lets sourcing rotate off a hammered yard PROACTIVELY, before the next
// market re-scan reflects the price climb it already rode (the live-observed pathology: 10
// consecutive buys at one yard, +20.5%, and no rotation, because SELECTION only ever sees the
// stale scan between re-scans). It is a SELECTION-only re-ranking signal: the money guards still
// gate on the yard's real scanned/live price, never this inflated number (RULINGS #4 — this
// never creates a new spend path and never changes WHETHER a probe is bought).
const (
	estImpactPerBuyCredits = 5_000
	impactDecayWindow      = 2 * time.Hour
)

// ProbePurchaser prices and buys one probe through the existing purchase_ship mediator
// path (RULINGS #3, the daemon is the single writer). It is DEMAND-PROXIMAL but BUYER-REACHABLE:
// given a target system it spawns the probe at the scanned probe-yard NEAREST that system (fewest
// gate hops, arbitrated against price by the caller's tunable) WHEN an idle undedicated buyer can
// relay to it, so the reconciler's relay is short instead of always buying at the home yard. When
// the near-target yard is beyond every buyer's jump-reach it FALLS BACK to the nearest reachable
// yard from a buyer's OWN system first (sp-xvi15 — reachability is anchored where a buyer IS, not at
// the deep target the relay cannot route to, so a buy never retry-loops a yard beyond reach while a
// reachable current-era yard sits in inventory). The purchase runs through an idle, UNDEDICATED hull
// (never a pinned or working one — RULINGS #7) chosen to REACH the yard; a home-yard buy uses an
// in-place hull movement-free. FAIL-OPEN by construction: no target, no finder, no located buyer, an
// empty/sparse scan store, or nothing buyer-reachable all fall back to the home yard — missing
// shipyard data never fails a buy closed.
type ProbePurchaser struct {
	mediator   common.Mediator
	shipRepo   navigation.ShipRepository
	yardFinder probeYardFinder
	ledger     ledger.TransactionRepository
	clock      shared.Clock
	// ownFleet is the dedicated-fleet tag whose hulls THIS purchaser may drive as buyers, in
	// addition to undedicated hulls. Default "" = undedicated-only, byte-identical to
	// every pre-existing caller (frontier + freshness sizer set nothing). Set to "probe-buyer" so the
	// stationed probe-buyer coordinator reuses this exact buy path over its OWN dedicated hulls — the
	// buyer selection accepts them AND the single-writer claim is taken under their fleet operation so
	// ClaimShip's dedication guard passes. Every OTHER caller keeps ownFleet="" and so keeps skipping
	// ALL dedicated hulls, so a probe-buyer hull stays "driven by one coordinator, skipped by the rest".
	ownFleet string
}

// NewProbePurchaser wires the price-and-buy port. yardFinder is optional (nil disables
// target-aware selection — every buy stays on the home-yard in-place path). txnLedger backs the
// per-yard recent-buy price-impact term (unconditional — see recentBuyImpact); clock is
// nil-safe (nil -> real clock).
func NewProbePurchaser(mediator common.Mediator, shipRepo navigation.ShipRepository, yardFinder probeYardFinder, txnLedger ledger.TransactionRepository, clock shared.Clock) *ProbePurchaser {
	return &ProbePurchaser{mediator: mediator, shipRepo: shipRepo, yardFinder: yardFinder, ledger: txnLedger, clock: clock}
}

// SetOwnFleet sets the dedicated-fleet tag whose idle hulls this purchaser may also use as buyers
// (sp-f082y), returning the receiver for fluent wiring. Default "" leaves the purchaser
// undedicated-only (byte-identical to every existing caller). The stationed probe-buyer coordinator
// wires a dedicated instance with SetOwnFleet("probe-buyer") so it reuses the whole buy path over its
// own hulls without forking it. See the ownFleet field doc.
func (p *ProbePurchaser) SetOwnFleet(fleet string) *ProbePurchaser {
	p.ownFleet = fleet
	return p
}

// drivableBuyer reports whether an idle hull may be driven as a probe buyer: an UNDEDICATED hull, or
// one dedicated to ownFleet (the caller's own fleet, when set). A hull dedicated to ANOTHER fleet is
// never poached (RULINGS #7). ownFleet=="" degenerates to "undedicated only" — exactly the before
// `DedicatedFleet() != ""` skip, so every existing caller is byte-identical.
func drivableBuyer(ship *navigation.Ship, ownFleet string) bool {
	fleet := ship.DedicatedFleet()
	return fleet == "" || fleet == ownFleet
}

// probeBuyPlan is one resolved buy: the buyer hull, the yard to buy at, and the price the guards
// judge. relay marks a demand-proximal plan whose buyer must be RELAYED to the yard (and the live
// dock price re-checked) before the buy; a home in-place plan leaves relay false and buys where the
// buyer already sits. buyerAtYard (relay plans only) records that the resolved buyer already sits at
// the yard, so the relay is skipped movement-free while the dock price is still re-checked.
type probeBuyPlan struct {
	buyer       string
	yard        string
	price       int
	relay       bool
	buyerAtYard bool
}

// QuoteProbe returns the demand-proximal probe price and yard: the scanned probe-yard nearest
// target, or — with no target / no finder / no scanned yard — the cheapest live in-place
// yard exactly as before. The price feeds the caller's unchanged money guards.
func (p *ProbePurchaser) QuoteProbe(ctx context.Context, playerID shared.PlayerID, target probebuy.ProbeTarget) (int, string, error) {
	plan, err := p.resolveProbeBuy(ctx, playerID, target)
	if err != nil {
		return 0, "", err
	}
	return plan.price, plan.yard, nil
}

// BuyProbe purchases one probe at the demand-proximal (or fallback home) yard, refusing a fill
// whose price exceeds maxBudget (the 25% treasury ceiling the coordinator computed). A target-yard
// plan RELAYS an idle undedicated hull to the winning yard (never a movement-free buy
// where a hull happens to sit) and RE-CHECKS the live dock price
// there so the guard judges the ACTUAL price and not a stale scan; a home-yard fallback plan buys
// through the in-place hull movement-free at its already-live price. It verifies the yard delivered
// a probe and not a substituted hull (money integrity), then returns the price paid and the
// new hull's symbol.
func (p *ProbePurchaser) BuyProbe(ctx context.Context, playerID shared.PlayerID, maxBudget int, target probebuy.ProbeTarget) (int, string, error) {
	plan, err := p.resolveProbeBuy(ctx, playerID, target)
	if err != nil {
		return 0, "", err
	}

	// RULINGS #3 single-writer: hold an EXCLUSIVE journey claim on the buyer for the WHOLE
	// source→yard reposition, so the OTHER probe-buyer coordinator can never grab this same idle hull
	// mid-relay and desync the no-reload multi-hop jump into a physically-impossible self-jump
	// (sp-1bme8). The claim is the atomic, row-locked ShipRepository primitive — a lost race fails the
	// buy CLOSED (RULINGS #4 safe: no concurrent second driver, no bad spend). Released on EVERY exit
	// below; a claim stranded by a crash is freed by the daemon's boot-time ReleaseAllActive (RULINGS
	// #2 restart-idempotent). Once the journey holds this real claim, the gate-crosser's existing
	// SkipClaim hops trust a claim that now genuinely exists.
	releaseClaim, err := p.claimBuyer(ctx, playerID, plan.buyer, target.ClaimOwnerContainerID)
	if err != nil {
		return 0, "", err
	}
	defer releaseClaim()

	price := plan.price
	if plan.relay { // demand-proximal plan: relay the resolved buyer to the yard, then re-price at the dock
		price, err = p.relayAndRecheck(ctx, playerID, plan)
		if err != nil {
			return 0, "", err
		}
	}

	// Guard on the ACTUAL price the buy will pay: the live dock re-check for a relayed
	// target-yard buy, or the already-live in-place price for a home-yard fallback — never a stale
	// scan that lets a depleted yard slip past the 25% treasury ceiling.
	if price > maxBudget {
		return 0, "", fmt.Errorf("probe price %d exceeds budget %d", price, maxBudget)
	}

	resp, err := p.mediator.Send(ctx, &shipyardCmd.PurchaseShipCommand{
		PurchasingShipSymbol: plan.buyer,
		ShipType:             probeShipType,
		PlayerID:             playerID,
		ShipyardWaypoint:     plan.yard, // buyer is now AT the yard (relayed or in-place) → movement-free
	})
	if err != nil {
		return 0, "", err
	}
	r, ok := resp.(*shipyardCmd.PurchaseShipResponse)
	if !ok || r.Ship == nil {
		return 0, "", fmt.Errorf("unexpected purchase response")
	}
	if r.ShipType != probeShipType {
		return 0, "", fmt.Errorf("yard delivered %q, not %q", r.ShipType, probeShipType)
	}
	return r.PurchasePrice, r.Ship.ShipSymbol(), nil
}

// claimBuyer takes the EXCLUSIVE, atomic single-writer claim on the buyer hull for the whole
// probe-buy journey (RULINGS #3) and returns a release closure that is always safe to
// defer — the underlying ReleaseContainerClaim is idempotent (a no-op on an already-idle hull). A
// lost claim race surfaces as an error so BuyProbe fails CLOSED, never a concurrent second driver.
// owner defaults to a well-known label so the claim is taken even when the caller wires no container
// id (the exclusivity never silently degrades). The claim reuses the SAME row-locked
// ShipRepository primitive every coordinator claims work through — no new table or mechanism.
func (p *ProbePurchaser) claimBuyer(ctx context.Context, playerID shared.PlayerID, buyer, owner string) (func(), error) {
	if owner == "" {
		owner = defaultProbeBuyClaimOwner
	}
	// The claim operation is the buyer's OWN fleet when driving a dedicated probe-buyer hull, so
	// ClaimShip's dedication guard (DedicatedFleet != "" && != operation) accepts it instead of
	// rejecting it; for the default undedicated buyer the operation stays the plain probe_buy label
	// (the guard is a no-op on an undedicated hull either way). Byte-identical when ownFleet is "".
	operation := probeBuyClaimOperation
	if p.ownFleet != "" {
		operation = p.ownFleet
	}
	if err := p.shipRepo.ClaimShip(ctx, buyer, owner, playerID, operation); err != nil {
		return nil, fmt.Errorf("probe buyer %s claim failed (fail-closed, no concurrent driver): %w", buyer, err)
	}
	return func() {
		if _, err := p.shipRepo.ReleaseContainerClaim(ctx, buyer, playerID, probeBuyClaimReleaseReason); err != nil {
			common.LoggerFromContext(ctx).Log("WARN", "probe buyer journey claim release failed (boot ReleaseAllActive is the backstop)", map[string]interface{}{
				"action":      "probe_buy_claim_release",
				"ship_symbol": buyer,
				"error":       err.Error(),
			})
		}
	}, nil
}

// resolveProbeBuy picks the buy yard: the demand-proximal scanned yard nearest the target when one
// is known, else the home-yard in-place buyer. The proximal branch FAILS OPEN — any gap (no
// target, no finder, empty/unreadable scan store) returns ok=false and the in-place fallback runs,
// so missing shipyard data can never fail a buy closed.
func (p *ProbePurchaser) resolveProbeBuy(ctx context.Context, playerID shared.PlayerID, target probebuy.ProbeTarget) (probeBuyPlan, error) {
	if plan, ok := p.resolveProximalBuy(ctx, playerID, target); ok {
		return plan, nil
	}
	return p.resolveInPlaceBuy(ctx, playerID)
}

// resolveProximalBuy selects a buy yard REACHABLE from an idle undedicated buyer hull, anchoring
// reachability where a buyer actually IS — not at the deep target the relay cannot route to (sp-xvi15:
// the demand-proximal winner is often beyond the buyer's jump-reach, so relaying to it fail-closes
// every cycle and the fleet stalls at its cap). It KEEPS the demand-proximal winner when a buyer can
// reach it (the unchanged sp-4m4ve near-target selection), else FALLS BACK to the nearest reachable
// yard from a buyer's own system first, so a buy never retry-loops a yard beyond reach while a
// reachable current-era yard sits in inventory. The resolved buyer PROVABLY reaches the chosen yard,
// so the later relay routes. ok=false fails OPEN to the home in-place path for every gap (no target,
// no finder, no located buyer, an empty/unreadable scan store, nothing buyer-reachable) — missing
// shipyard data never fails a buy closed.
func (p *ProbePurchaser) resolveProximalBuy(ctx context.Context, playerID shared.PlayerID, target probebuy.ProbeTarget) (probeBuyPlan, bool) {
	if target.System == "" || p.yardFinder == nil {
		return probeBuyPlan{}, false
	}
	buyers := p.eligibleBuyers(ctx, playerID)
	if len(buyers) == 0 {
		return probeBuyPlan{}, false // no located undedicated hull to relay → fall open to home
	}
	reachBySystem := p.reachableYardsByBuyerSystem(ctx, playerID, buyers)
	if len(reachBySystem) == 0 {
		return probeBuyPlan{}, false // no buyer system reaches a priced yard → fall open to home
	}
	impact := p.recentBuyImpact(ctx, playerID)
	chosen, ok := p.pickReachableYard(ctx, playerID, target, reachBySystem, impact)
	if !ok {
		return probeBuyPlan{}, false
	}
	buyer, ok := selectRelayBuyer(buyers, reachBySystem, chosen)
	if !ok {
		return probeBuyPlan{}, false
	}
	return probeBuyPlan{
		buyer:       buyer.ShipSymbol(),
		yard:        chosen.WaypointSymbol,
		price:       chosen.PurchasePrice,
		relay:       true,
		buyerAtYard: buyerAtYard(buyer, chosen.WaypointSymbol),
	}, true
}

// eligibleBuyers returns the idle, UNDEDICATED, located hulls a probe buy may relay (RULINGS #7 —
// never poach a dedicated hull). A hull with no location cannot anchor reachability and is skipped;
// a fleet read miss yields none (fail open to home).
func (p *ProbePurchaser) eligibleBuyers(ctx context.Context, playerID shared.PlayerID) []*navigation.Ship {
	ships, err := p.shipRepo.FindIdleByPlayer(ctx, playerID)
	if err != nil {
		return nil
	}
	buyers := make([]*navigation.Ship, 0, len(ships))
	for _, ship := range ships {
		if !drivableBuyer(ship, p.ownFleet) {
			continue
		}
		if ship.CurrentLocation() == nil {
			continue
		}
		buyers = append(buyers, ship)
	}
	return buyers
}

// reachableYardsByBuyerSystem runs ONE per-system finder read for each DISTINCT buyer system,
// returning the priced probe-yards reachable from that system (ranked hops-then-price). Anchoring
// reachability per buyer SYSTEM — not at the deep target — is the fix: a yard here is one an actual
// buyer can relay to. A per-system read error or an empty rank drops that system (it contributes no
// reachable yard), never a whole-buy failure — fail open.
func (p *ProbePurchaser) reachableYardsByBuyerSystem(ctx context.Context, playerID shared.PlayerID, buyers []*navigation.Ship) map[string][]shipyardQueries.YardCandidate {
	reach := make(map[string][]shipyardQueries.YardCandidate)
	for _, buyer := range buyers {
		system := buyer.CurrentLocation().SystemSymbol
		if _, seen := reach[system]; seen {
			continue
		}
		candidates, err := p.yardFinder.NearestYardsSelling(ctx, playerID.Value(), []string{probeShipType}, []string{system})
		if err != nil || len(candidates) == 0 {
			continue
		}
		reach[system] = candidates
	}
	return reach
}

// pickReachableYard chooses the buy yard from the buyer-reachable set: the DEMAND-PROXIMAL winner
// when a buyer can reach it (the unchanged sp-4m4ve selection, so a reachable near-target yard still
// wins), else the NEAREST reachable yard from any buyer system — the buyer's own system first, a
// 0-hop same-system yard the hop penalty prefers. ok=false only when the reachable union is empty
// (guarded by the caller).
func (p *ProbePurchaser) pickReachableYard(ctx context.Context, playerID shared.PlayerID, target probebuy.ProbeTarget, reachBySystem map[string][]shipyardQueries.YardCandidate, impact map[string]int) (shipyardQueries.YardCandidate, bool) {
	if demand := p.demandProximalReachable(ctx, playerID, target, reachableWaypoints(reachBySystem)); len(demand) > 0 {
		return pickBuyYard(demand, target.HopPenaltyCredits, target.SiblingPriceMarginCredits, impact), true
	}
	union := mergeReachByMinHops(reachBySystem)
	if len(union) == 0 {
		return shipyardQueries.YardCandidate{}, false
	}
	return pickBuyYard(union, target.HopPenaltyCredits, target.SiblingPriceMarginCredits, impact), true
}

// demandProximalReachable ranks the demand-proximal candidates (nearest the TARGET system) but keeps
// only those a buyer can actually reach — the intersection that preserves the near-target win WHEN it
// is relayable. An unreadable target rank yields none, so the caller falls back to nearest-reachable.
func (p *ProbePurchaser) demandProximalReachable(ctx context.Context, playerID shared.PlayerID, target probebuy.ProbeTarget, reachable map[string]bool) []shipyardQueries.YardCandidate {
	candidates, err := p.yardFinder.NearestYardsSelling(ctx, playerID.Value(), []string{probeShipType}, []string{target.System})
	if err != nil {
		return nil
	}
	demand := make([]shipyardQueries.YardCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if reachable[candidate.WaypointSymbol] {
			demand = append(demand, candidate)
		}
	}
	return demand
}

// reachableWaypoints flattens the per-system reachable yards to the set of reachable yard waypoints.
func reachableWaypoints(reachBySystem map[string][]shipyardQueries.YardCandidate) map[string]bool {
	set := make(map[string]bool)
	for _, candidates := range reachBySystem {
		for _, candidate := range candidates {
			set[candidate.WaypointSymbol] = true
		}
	}
	return set
}

// mergeReachByMinHops unions the per-system reachable yards, keeping each yard at its MINIMUM hop
// distance across buyer systems (the nearest buyer's reach), so the fallback rank prefers a yard in a
// buyer's own system (0 hops). The result is sorted hops-then-price-then-waypoint so a tie resolves
// deterministically (RULINGS #2), mirroring the finder's own rank.
func mergeReachByMinHops(reachBySystem map[string][]shipyardQueries.YardCandidate) []shipyardQueries.YardCandidate {
	best := make(map[string]shipyardQueries.YardCandidate)
	for _, candidates := range reachBySystem {
		for _, candidate := range candidates {
			if existing, ok := best[candidate.WaypointSymbol]; !ok || candidate.Hops < existing.Hops {
				best[candidate.WaypointSymbol] = candidate
			}
		}
	}
	union := make([]shipyardQueries.YardCandidate, 0, len(best))
	for _, candidate := range best {
		union = append(union, candidate)
	}
	sort.Slice(union, func(i, j int) bool {
		if union[i].Hops != union[j].Hops {
			return union[i].Hops < union[j].Hops
		}
		if union[i].PurchasePrice != union[j].PurchasePrice {
			return union[i].PurchasePrice < union[j].PurchasePrice
		}
		return union[i].WaypointSymbol < union[j].WaypointSymbol
	})
	return union
}

// selectRelayBuyer picks the hull that relays to the chosen yard, PROVABLY able to reach it: a hull
// already AT the yard (movement-free) first, then a hull in the yard's OWN system (a 0-hop in-system
// relay), then any hull whose system's reach includes the yard. ok=false only if no buyer reaches it
// (unreachable by construction — the caller then fails open to home).
func selectRelayBuyer(buyers []*navigation.Ship, reachBySystem map[string][]shipyardQueries.YardCandidate, chosen shipyardQueries.YardCandidate) (*navigation.Ship, bool) {
	var sameSystem, anyReaching *navigation.Ship
	for _, buyer := range buyers {
		loc := buyer.CurrentLocation()
		if loc.Symbol == chosen.WaypointSymbol {
			return buyer, true // already at the yard → movement-free
		}
		if !reaches(reachBySystem[loc.SystemSymbol], chosen.WaypointSymbol) {
			continue
		}
		if loc.SystemSymbol == chosen.SystemSymbol && sameSystem == nil {
			sameSystem = buyer
		}
		if anyReaching == nil {
			anyReaching = buyer
		}
	}
	if sameSystem != nil {
		return sameSystem, true
	}
	if anyReaching != nil {
		return anyReaching, true
	}
	return nil, false
}

// reaches reports whether a system's reachable-yard slice contains the chosen yard waypoint.
func reaches(candidates []shipyardQueries.YardCandidate, waypoint string) bool {
	for _, candidate := range candidates {
		if candidate.WaypointSymbol == waypoint {
			return true
		}
	}
	return false
}

// recentBuyImpactScan bounds the newest-first PURCHASE_SHIP rows scanned to derive each
// candidate yard's recent-buy count — generous enough to cover every probe buy that can fall
// inside a realistic impactDecayWindow, mirroring probebuy's windowProbeSpendScan bound.
const recentBuyImpactScan = 500

// recentBuyImpact derives, per yard waypoint, the SELECTION-only price-impact credits from this
// player's PERSISTED recent probe PURCHASE_SHIP transactions (RULINGS #2: reconstructed fresh
// from the ledger every call, never an in-memory counter a restart would lose). Returns nil (zero
// impact everywhere) when the ledger read fails or turns up no recent probe buys — this
// SELECTION signal fails OPEN, exactly like the yard finder above: it can never fail a buy closed
// (RULINGS #4 guards it never touches).
func (p *ProbePurchaser) recentBuyImpact(ctx context.Context, playerID shared.PlayerID) map[string]int {
	clock := p.clock
	if clock == nil {
		clock = shared.NewRealClock()
	}
	since := clock.Now().Add(-impactDecayWindow)
	shipPurchase := ledger.TransactionTypePurchaseShip
	txns, err := p.ledger.FindByPlayer(ctx, playerID, ledger.QueryOptions{
		TransactionType: &shipPurchase,
		StartDate:       &since,
		Limit:           recentBuyImpactScan,
	})
	if err != nil {
		return nil
	}
	counts := make(map[string]int)
	for _, t := range txns {
		shipType, _ := t.Metadata()["ship_type"].(string)
		if shipType != probeShipType {
			continue
		}
		waypoint, _ := t.Metadata()["waypoint"].(string)
		if waypoint == "" {
			continue
		}
		counts[waypoint]++
	}
	if len(counts) == 0 {
		return nil
	}
	impactByYard := make(map[string]int, len(counts))
	for waypoint, n := range counts {
		impactByYard[waypoint] = n * estImpactPerBuyCredits
	}
	return impactByYard
}

// pickBuyYard selects the buy yard across ALL reachable candidates: the demand-proximal
// hop-penalty winner, UNLESS a cheaper reachable sibling undercuts it by more than
// siblingMargin — the supply-depletion override. Repeated buys raise a yard's scanned
// price (LIMITED→SCARCE); the override abandons a yard the moment a sibling beats it by more than
// the margin, so the buy spreads instead of spiraling one market to 4x. A
// siblingMargin<=0 disables the override, degenerating to pure hop-penalty selection (rollback).
// impact (sp-4m4ve Phase 3 §2D) is the PROACTIVE counterpart: per-yard recent-buy price-impact
// credits, folded into each candidate's effective cost BEFORE both the hop-penalty ranking and
// the sibling-margin comparison, so consecutive buys rotate yards even between market re-scans. A
// nil/empty impact map adds zero everywhere — byte-identical to previous selection.
func pickBuyYard(candidates []shipyardQueries.YardCandidate, hopPenalty, siblingMargin int, impact map[string]int) shipyardQueries.YardCandidate {
	proximal := pickProximalYard(candidates, hopPenalty, impact)
	if siblingMargin <= 0 {
		return proximal
	}
	cheapest := cheapestYard(candidates, impact)
	if effectivePrice(proximal, impact)-effectivePrice(cheapest, impact) > siblingMargin {
		return cheapest // depleted near yard — spread the buy to the cheapest sibling
	}
	return proximal
}

// effectivePrice is a candidate's scanned PurchasePrice plus its per-yard recent-buy
// price-impact credit (0 for every yard while impact is nil/empty — the previous value).
func effectivePrice(c shipyardQueries.YardCandidate, impact map[string]int) int {
	return c.PurchasePrice + impact[c.WaypointSymbol]
}

// pickProximalYard chooses the scanned probe-yard minimizing effectiveCost = effectivePrice +
// Hops*hopPenalty — the demand-proximal tradeoff, proactively inflated per yard by the
// recent-buy impact term. A high penalty makes proximity dominate (buy NEAREST the post); a zero
// penalty degenerates to the cheapest reachable yard. The finder pre-sorts candidates
// hops-then-price, so a strict-less comparison keeps the FIRST minimum — the nearest, then
// cheapest — on a tie.
func pickProximalYard(candidates []shipyardQueries.YardCandidate, hopPenalty int, impact map[string]int) shipyardQueries.YardCandidate {
	best := candidates[0]
	bestCost := effectivePrice(best, impact) + best.Hops*hopPenalty
	for _, candidate := range candidates[1:] {
		cost := effectivePrice(candidate, impact) + candidate.Hops*hopPenalty
		if cost < bestCost {
			best, bestCost = candidate, cost
		}
	}
	return best
}

// cheapestYard returns the candidate with the lowest EFFECTIVE price — the sibling the
// supply-depletion override spreads a buy to when the proximity winner has been priced up (by the
// live scan, the recent-buy impact term, or both). Ties keep the first (the finder pre-sorts
// hops-then-price, so the nearest cheapest wins).
func cheapestYard(candidates []shipyardQueries.YardCandidate, impact map[string]int) shipyardQueries.YardCandidate {
	cheapest := candidates[0]
	for _, candidate := range candidates[1:] {
		if effectivePrice(candidate, impact) < effectivePrice(cheapest, impact) {
			cheapest = candidate
		}
	}
	return cheapest
}

// resolveInPlaceBuy scans idle, undedicated ships for one sitting at a shipyard that sells
// SHIP_PROBE, returning the cheapest such plan (buyer present, live price). Movement-free and
// poach-free by construction.
func (p *ProbePurchaser) resolveInPlaceBuy(ctx context.Context, playerID shared.PlayerID) (probeBuyPlan, error) {
	ships, err := p.shipRepo.FindIdleByPlayer(ctx, playerID)
	if err != nil {
		return probeBuyPlan{}, err
	}
	best := probeBuyPlan{}
	for _, ship := range ships {
		if !drivableBuyer(ship, p.ownFleet) {
			continue // never disturb a hull dedicated to ANOTHER fleet (RULINGS #7); the caller's own
			// probe-buyer fleet is drivable when ownFleet is set, byte-identical (undedicated-only) otherwise
		}
		loc := ship.CurrentLocation()
		if loc == nil {
			continue
		}
		// A SEARCH, not a guard read: this asks whether the waypoint this idle hull
		// happens to be parked at sells probes at all, and for most hulls the answer
		// is "there is no shipyard here" — which the cached trait filter answers for
		// free, but only for a DENIABLE read. A decline skips the hull, so the guard
		// below still only ever sees a LIVE price (RULINGS #4: strictly stricter, it
		// can prevent a buy and never authorise one).
		price, ok := p.probePriceAt(ctx, playerID, loc.SystemSymbol, loc.Symbol, marketscan.Discretionary)
		if !ok {
			continue
		}
		if best.buyer == "" || price < best.price {
			best = probeBuyPlan{buyer: ship.ShipSymbol(), yard: loc.Symbol, price: price}
		}
	}
	if best.buyer == "" {
		return probeBuyPlan{}, fmt.Errorf("no idle undedicated ship is stationed at a probe-selling shipyard")
	}
	return best, nil
}

// relayAndRecheck readies a demand-proximal buy: it RELAYS the plan's already-resolved buyer to the
// winning yard when it is not already there (the buyer was chosen to reach it), and RE-CHECKS the
// live dock price so the money guard judges the ACTUAL price rather than a stale scan. Returns the
// re-checked live price. A relay that cannot route, or a dock whose live price is unreadable after
// arrival, fails the buy CLOSED — the fail-OPEN home fallback covers only MISSING selection data, not
// a committed relay that then cannot be priced (never overpay blind).
func (p *ProbePurchaser) relayAndRecheck(ctx context.Context, playerID shared.PlayerID, plan probeBuyPlan) (int, error) {
	if !plan.buyerAtYard {
		if err := p.navigateBuyerToYard(ctx, playerID, plan.buyer, plan.yard); err != nil {
			return 0, err
		}
	}
	// PRE-COMMIT: the buyer has been relayed and the guard judges this exact number
	// before the purchase commits, so it must be live and undeniable (RULINGS #4).
	price, ok := p.probePriceAt(ctx, playerID, shared.ExtractSystemSymbol(plan.yard), plan.yard, marketscan.Earning)
	if !ok {
		return 0, fmt.Errorf("live dock price at %s unreadable after relay", plan.yard)
	}
	return price, nil
}

// navigateBuyerToYard relays the buyer to the winning yard via the shared high-level navigation
// command, which routes CROSS-SYSTEM through the gate-crosser wired on the NavigateRoute
// handler — a frontier yard is almost always in another system. A relay error surfaces so the buy
// fails closed rather than buying at the wrong (current) location.
func (p *ProbePurchaser) navigateBuyerToYard(ctx context.Context, playerID shared.PlayerID, buyerSymbol, yard string) error {
	if _, err := p.mediator.Send(ctx, &shipNav.NavigateRouteCommand{
		ShipSymbol:  buyerSymbol,
		Destination: yard,
		PlayerID:    playerID,
	}); err != nil {
		return fmt.Errorf("failed to relay probe buyer to %s: %w", yard, err)
	}
	return nil
}

// buyerAtYard reports whether the resolved hull already sits at the winning yard, so a relay is
// skipped (movement-free) when a hull happens to be present there already.
func buyerAtYard(buyer *navigation.Ship, yard string) bool {
	loc := buyer.CurrentLocation()
	return loc != nil && loc.Symbol == yard
}

// probePriceAt reads the live SHIP_PROBE purchase price at a waypoint's shipyard, or
// reports ok=false if the waypoint is not a probe-selling shipyard (or has no priced
// listing — which happens unless a ship is present, exactly the in-place buyers we scan).
// class decides whether the read may be paced. The two callers differ and the
// difference is not cosmetic: relayAndRecheck prices the ONE yard the buy is
// about to commit at (Earning — undeniable, RULINGS #4), while resolveInPlaceBuy
// SWEEPS every idle hull's current waypoint looking for a counter that sells
// probes at all (Discretionary — a search, and one whose waypoints are mostly not
// shipyards). Collapsing both onto Earning is what let a fleet-wide sweep bypass
// the trait filter, the rescan floor and the allowance together.
func (p *ProbePurchaser) probePriceAt(ctx context.Context, playerID shared.PlayerID, systemSymbol, waypointSymbol string, class marketscan.Class) (int, bool) {
	resp, err := p.mediator.Send(ctx, &shipyardQueries.GetShipyardListingsQuery{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: waypointSymbol,
		PlayerID:       playerID,
		Class:          class,
	})
	if err != nil {
		return 0, false
	}
	r, ok := resp.(*shipyardQueries.GetShipyardListingsResponse)
	if !ok {
		return 0, false
	}
	listing, found := r.Shipyard.FindListingByType(probeShipType)
	if !found || listing.PurchasePrice <= 0 {
		return 0, false
	}
	return listing.PurchasePrice, true
}

// ---- ProbeBuyerPositioner (sp-255rz: break the "probe unpriceable" stall) --

// ProbeBuyerPositioner breaks the "probe unpriceable" stall (sp-255rz). When the frontier
// coordinator's QuoteProbe fails closed because no idle undedicated hull sits at a probe-selling
// shipyard and no priced yard is reachable from the deep target, fleet growth halts forever once the
// fleet drifts from yards. This relays an eligible hull to the nearest reachable SCANNED probe-yard
// from the HULL's OWN system, so the NEXT tick's presence-gated live price reads and the in-place buy
// clears. Reachability is anchored where the hull IS (not at the deep target the proximal path failed
// on) — the load-bearing difference from the buy path. It reuses the SAME collaborators as the
// ProbePurchaser (the ReachableYardFinder + ship repo + navigation mediator) — no new subsystem. It
// mirrors the home-shipyard positioner: NEVER buys, NEVER weakens a money guard (RULINGS #4),
// and NEVER poaches a dedicated or in-transit hull (RULINGS #7). Fail-safe + idempotent: a nil finder,
// a fleet read miss, no reachable yard, a hull already at the yard, or an in-flight relay all no-op
// (dispatched=false, no error) — only an unroutable relay surfaces an error.
type ProbeBuyerPositioner struct {
	mediator   common.Mediator
	shipRepo   navigation.ShipRepository
	yardFinder probeYardFinder
	// ownFleet mirrors ProbePurchaser.ownFleet: the dedicated-fleet tag whose idle hulls this
	// positioner may relay to a yard, in addition to undedicated ones. Default "" =
	// undedicated-only, byte-identical to the pre-existing frontier caller. The probe-buyer
	// coordinator wires SetOwnFleet("probe-buyer") so it stations its OWN dedicated buyers via the
	// same relay path; every other caller keeps skipping all dedicated hulls.
	ownFleet string
}

// NewProbeBuyerPositioner wires the stall breaker over the same seams as the purchaser. yardFinder is
// optional (nil disables positioning — the coordinator stays fail-closed byte-identical).
func NewProbeBuyerPositioner(mediator common.Mediator, shipRepo navigation.ShipRepository, yardFinder probeYardFinder) *ProbeBuyerPositioner {
	return &ProbeBuyerPositioner{mediator: mediator, shipRepo: shipRepo, yardFinder: yardFinder}
}

// SetOwnFleet sets the dedicated-fleet tag whose idle hulls this positioner may also relay to a yard
// (sp-f082y), returning the receiver for fluent wiring. Default "" is undedicated-only (byte-identical
// to the frontier caller). See the ownFleet field doc.
func (p *ProbeBuyerPositioner) SetOwnFleet(fleet string) *ProbeBuyerPositioner {
	p.ownFleet = fleet
	return p
}

// PositionProbeBuyer relays an eligible idle undedicated hull to the nearest reachable probe-yard from
// its own system. dispatched=false (no relay) whenever the fix cannot safely act.
func (p *ProbeBuyerPositioner) PositionProbeBuyer(ctx context.Context, playerID shared.PlayerID) (bool, error) {
	if p.yardFinder == nil {
		return false, nil // no finder wired → cannot locate a probe-yard (fail-safe no-op)
	}
	ships, err := p.shipRepo.FindIdleByPlayer(ctx, playerID)
	if err != nil {
		return false, nil // a fleet read miss never crashes the cycle — retry next tick
	}
	// Eligible buyer = idle, NOT dedicated (RULINGS #7 — never poach a contract hauler / mfg worker),
	// NOT in transit, and located. An undedicated hull already IN TRANSIT is a prior positioning relay
	// under way: bail so we never send a second hull (bounds in-flight to one, idempotent — RULINGS #2).
	eligible := make([]*navigation.Ship, 0, len(ships))
	for _, ship := range ships {
		if !drivableBuyer(ship, p.ownFleet) {
			continue // a hull dedicated to ANOTHER fleet is never disturbed; the caller's own probe-buyer
			// fleet is drivable when ownFleet is set, byte-identical (undedicated-only) otherwise
		}
		if ship.IsInTransit() {
			return false, nil // an undedicated hull is already relaying — wait, do not double-send
		}
		if ship.CurrentLocation() == nil {
			continue
		}
		eligible = append(eligible, ship)
	}
	if len(eligible) == 0 {
		return false, nil // no free undedicated hull to position this cycle (fail-safe no-op)
	}
	// Relay the first eligible hull that can reach a scanned probe-yard from its OWN system. Per-hull
	// (not a multi-source union) keeps the yard reachable from the hull actually sent.
	for _, ship := range eligible {
		loc := ship.CurrentLocation()
		candidates, ferr := p.yardFinder.NearestYardsSelling(ctx, playerID.Value(), []string{probeShipType}, []string{loc.SystemSymbol})
		if ferr != nil || len(candidates) == 0 {
			continue // no reachable priced probe-yard from this hull's system — try the next hull
		}
		targetYard := candidates[0].WaypointSymbol
		if loc.Symbol == targetYard {
			return false, nil // a hull already sits at the yard — the live price reads next tick, no relay
		}
		if _, nerr := p.mediator.Send(ctx, &shipNav.NavigateRouteCommand{
			ShipSymbol:  ship.ShipSymbol(),
			Destination: targetYard,
			PlayerID:    playerID,
		}); nerr != nil {
			return false, fmt.Errorf("failed to position probe buyer %s at yard %s: %w", ship.ShipSymbol(), targetYard, nerr)
		}
		return true, nil
	}
	return false, nil // no eligible hull can reach a probe-selling yard this cycle (fail-safe no-op)
}

// ---- ExpansionScanner ------------------------------------------------------

// GateGraph is the slice of gategraph.Service the scanner uses. Adjacency is the whole
// persisted cross-system gate graph in one era-scoped read (what the BFS walks). Connections
// is the per-system fetch-through read that CHARTS a covered frontier system's own jump gate
// on a miss (a single live GetJumpGate that persists the edge set) — the seam that grows the
// walkable graph one ring at a time so expansion does not freeze at the last charted ring.
// The real *gategraph.Service satisfies both.
type GateGraph interface {
	Adjacency(ctx context.Context) (map[string][]system.GateEdge, error)
	Connections(ctx context.Context, systemSymbol string, playerID int) ([]system.GateEdge, error)
}

// WaypointCatalog is the narrow slice of system.WaypointRepository the scanner reads to decide
// whether a system's full waypoint set was SWEPT. ListBySystem returns the persisted
// waypoint rows for a system — non-empty (with real bodies) only after a sweep persisted them
// via BuildSystemGraph. Narrowing to the one method keeps the port intent-revealing and the
// scanned-discriminator trivially fakeable.
type WaypointCatalog interface {
	ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error)
}

// ExpansionScanner enumerates the gate-reachable frontier for the coordinator's queue.
// It runs one multi-source BFS over the persisted gate adjacency from the anchor set
// (HQ + the systems the fleet currently occupies), bounded by maxHops, and annotates
// each reached system with its known-market count, a charted flag, and a scanned flag
// (whether its full waypoint set was swept). Charted-only reachability: virgin
// systems surface as edge targets of a charted neighbor (a probe relayed there charts it
// on arrival).
type ExpansionScanner struct {
	gateGraph    GateGraph
	marketRepo   market.MarketRepository
	shipRepo     navigation.ShipRepository
	playerRepo   player.PlayerRepository
	waypointRepo WaypointCatalog
}

// NewExpansionScanner wires the frontier enumerator.
func NewExpansionScanner(
	gateGraph GateGraph,
	marketRepo market.MarketRepository,
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	waypointRepo WaypointCatalog,
) *ExpansionScanner {
	return &ExpansionScanner{
		gateGraph:    gateGraph,
		marketRepo:   marketRepo,
		shipRepo:     shipRepo,
		playerRepo:   playerRepo,
		waypointRepo: waypointRepo,
	}
}

// ExpansionCandidates returns every gate-reachable system within maxHops of the anchor
// set, annotated with hop distance, known-market count, and a charted flag.
func (s *ExpansionScanner) ExpansionCandidates(ctx context.Context, playerID int, maxHops int) ([]expansionCmd.ExpansionCandidate, error) {
	adj, err := s.gateGraph.Adjacency(ctx)
	if err != nil {
		return nil, fmt.Errorf("gate adjacency unreadable: %w", err)
	}

	anchors := s.anchorSystems(ctx, playerID)
	if len(anchors) == 0 {
		return nil, nil // no anchor to measure from → no queue this cycle
	}

	// Grow the walkable graph one ring BEFORE the BFS enumerates candidates. The
	// sweep charts a scouted frontier system's markets but not its jump gate, so its onward
	// edges never reach the persisted adjacency and the BFS below would dead-end at the last
	// charted ring — the frozen frontier / empty queue. Charting the covered ring's gate here
	// (fetch-through+persist, at most once per system) lets the BFS reach the next ring.
	s.growReachableFrontierGates(ctx, playerID, adj, anchors, maxHops)

	hops := bfsHops(adj, anchors, maxHops)
	// Corridor (branch) identity per reachable system — the depth slice's bearing.
	branchRoots := bfsBranchRoots(adj, anchors, maxHops)

	candidates := make([]expansionCmd.ExpansionCandidate, 0, len(hops))
	for sys, h := range hops {
		marketCount := s.knownMarketCount(ctx, playerID, sys)
		_, hasEdges := adj[sys]
		charted := hasEdges || marketCount > 0
		candidates = append(candidates, expansionCmd.ExpansionCandidate{
			SystemSymbol: sys,
			Hops:         h,
			KnownMarkets: marketCount,
			Charted:      charted,
			Scanned:      s.systemScanned(ctx, sys),
			BranchRoot:   branchRoots[sys],
		})
	}
	return candidates, nil
}

// anchorSystems is the set of systems hop distance is measured FROM: the agent's HQ
// system plus every system the fleet currently occupies (where we already operate).
func (s *ExpansionScanner) anchorSystems(ctx context.Context, playerID int) map[string]bool {
	anchors := make(map[string]bool)

	// Read through the shared player.HeadquartersFrom so this and the sensing
	// HomeSystemPort cannot disagree about which key holds the home waypoint or what counts as
	// present. Note this reader DEGRADES rather than failing: with no headquarters it silently
	// falls back to ship locations alone, so hop distance is measured from wherever the fleet
	// happens to be instead of from home. That is quieter than the sensing cutover's refusal and
	// therefore easier to miss — the anchor set simply looks smaller than it should.
	if p, err := s.playerRepo.FindByID(ctx, shared.MustNewPlayerID(playerID)); err == nil && p != nil {
		if hq, ok := player.HeadquartersFrom(p.Metadata); ok {
			anchors[shared.ExtractSystemSymbol(hq)] = true
		}
	}

	if ships, err := s.shipRepo.FindAllByPlayer(ctx, shared.MustNewPlayerID(playerID)); err == nil {
		for _, ship := range ships {
			if loc := ship.CurrentLocation(); loc != nil && loc.SystemSymbol != "" {
				anchors[loc.SystemSymbol] = true
			}
		}
	}
	return anchors
}

// knownMarketCount is the number of known marketplace waypoints in a system — the queue's
// "known markets" ranking signal. A virgin (uncharted) system returns 0.
func (s *ExpansionScanner) knownMarketCount(ctx context.Context, playerID int, systemSymbol string) int {
	markets, err := s.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return 0
	}
	return len(markets)
}

// systemScanned reports whether a system's FULL waypoint set has been SWEPT — the signal
// distinct from KnownMarkets (market_data rows exist only for systems that HAVE a market, so it
// cannot tell a swept-but-barren system from a never-scanned one) and from Charted (a gate edge
// means reachable, not swept). It reads the persisted waypoint catalog and asks hasNonGateWaypoint
// whether a real body was recorded (a sweep persists the whole set via BuildSystemGraph; gate
// charting persists edges, not waypoints). An unreadable catalog fails SAFE to not-scanned: the
// system stays a scout target rather than being wrongly dropped as barren.
func (s *ExpansionScanner) systemScanned(ctx context.Context, systemSymbol string) bool {
	waypoints, err := s.waypointRepo.ListBySystem(ctx, systemSymbol)
	if err != nil {
		return false
	}
	return hasNonGateWaypoint(waypoints)
}

// growReachableFrontierGates charts+persists the jump-gate edges of covered frontier systems
// so the reachability graph grows one ring per cycle. It supplies growFrontierGraph
// with the two live collaborators: a charted-predicate (a system a probe has SWEPT has known
// markets — only then is its gate chartable; a virgin system's live gate 400s "no ship
// present") and the fetch-through Connections call (a miss triggers one live GetJumpGate that
// PERSISTS the edge set, so the fetch is amortized to at most once per system).
func (s *ExpansionScanner) growReachableFrontierGates(ctx context.Context, playerID int, adj map[string][]system.GateEdge, anchors map[string]bool, maxHops int) {
	growFrontierGraph(adj, anchors, maxHops,
		func(systemSymbol string) bool { return s.knownMarketCount(ctx, playerID, systemSymbol) > 0 },
		func(systemSymbol string) ([]system.GateEdge, error) {
			return s.gateGraph.Connections(ctx, systemSymbol, playerID)
		},
	)
}

// hasNonGateWaypoint reports whether a system's persisted waypoint rows prove its FULL set was
// actually SWEPT, as opposed to merely gate-charted. It is true iff at least one
// persisted waypoint is NOT a jump gate. The ONLY writer of waypoint rows is BuildSystemGraph
// (graph_builder.go), which persists a system's ENTIRE paginated waypoint list — planets, moons,
// asteroids, the gate — the moment a probe sweeps it; gate-charting persists jump-gate EDGES to
// the gate_edges table (gategraph Service.fetchAndStore → store.Replace), never a waypoints row.
// So a persisted non-gate waypoint is proof of a real sweep, whereas a never-swept system has no
// such row. Requiring a NON-gate row (not merely "≥1 row") is deliberate: even if a lone
// jump-gate row were ever persisted, a gate-only-charted system must still read as NOT scanned so
// it stays a scout target. It is pure over its input (a resolved waypoint list), so the
// discriminator is unit-testable with no store, mirroring bfsHops/growFrontierGraph.
func hasNonGateWaypoint(waypoints []*shared.Waypoint) bool {
	for _, waypoint := range waypoints {
		if !waypoint.IsJumpGate() {
			return true
		}
	}
	return false
}

// bfsHops runs a multi-source BFS over the gate adjacency from the anchor set, returning
// each reachable system's minimum hop distance (<= maxHops). Under-construction edges are
// skipped (a multi-jump route can never traverse them) — Adjacency does not
// filter them, so the BFS must. Anchors are included at hop 0.
func bfsHops(adj map[string][]system.GateEdge, anchors map[string]bool, maxHops int) map[string]int {
	hops := make(map[string]int)
	type node struct {
		system string
		depth  int
	}
	queue := make([]node, 0, len(anchors))
	for a := range anchors {
		hops[a] = 0
		queue = append(queue, node{system: a, depth: 0})
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxHops {
			continue
		}
		for _, edge := range adj[cur.system] {
			if edge.UnderConstruction {
				continue
			}
			next := edge.ConnectedSystem
			if next == "" {
				continue
			}
			nd := cur.depth + 1
			if existing, seen := hops[next]; seen && existing <= nd {
				continue
			}
			hops[next] = nd
			queue = append(queue, node{system: next, depth: nd})
		}
	}
	return hops
}

// growFrontierGraph grows the walkable gate graph by ONE ring, mutating adj in place.
// The frozen-frontier bug: a scouted ring's MARKETS are charted by the sweep but
// its JUMP GATE is not, so its onward edges never enter the persisted adjacency and the
// multi-source BFS dead-ends at the last charted ring — the expansion queue empties. For each
// system reachable within the hop bound that is CHARTED (a probe reached it) but whose OWN
// gate edges are not yet persisted, it fetches them once via fetchConnections (fetch-through:
// a miss persists the edge set) and merges them into adj, so the following bfsHops reaches the
// next ring's virgins.
//
// It is pure over its inputs — the scanner supplies the real charted-predicate (known-market
// count) and fetch (gategraph Connections) — so the ring-growth is unit-testable with no
// store, API, or repo, mirroring bfsHops.
//
// API-frugality is structural, so a wide frontier is never stormed:
//   - a system already carrying edges is never fetched (served from the map, zero API);
//   - an UNCHARTED system (charted==false — no probe has arrived) is never probed, because its
//     live gate would 400 "no ship present" and trip the negative-result backoff;
//   - a system AT the hop bound is never charted onward (its neighbors fall beyond maxHops and
//     the bounded BFS would discard them).
//
// Each qualifying system is therefore fetched at most ONCE ever: the next cycle it carries
// persisted edges and is skipped. A fetch error (an unreadable / backed-off gate) leaves the
// node ungrown — the BFS already treats it as a dead-end — so it is swallowed here.
func growFrontierGraph(
	adj map[string][]system.GateEdge,
	anchors map[string]bool,
	maxHops int,
	charted func(systemSymbol string) bool,
	fetchConnections func(systemSymbol string) ([]system.GateEdge, error),
) {
	reachable := bfsHops(adj, anchors, maxHops)
	for systemSymbol, hop := range reachable {
		if hop >= maxHops {
			continue // its onward neighbors would fall beyond the bound — not worth a fetch
		}
		if _, hasEdges := adj[systemSymbol]; hasEdges {
			continue // gate already persisted — never re-fetch (frugality)
		}
		if !charted(systemSymbol) {
			continue // uncharted virgin — its live gate would 400/back off; leave it for a probe
		}
		edges, err := fetchConnections(systemSymbol)
		if err != nil || len(edges) == 0 {
			continue // unreadable/backed-off — the graph just doesn't grow through it
		}
		adj[systemSymbol] = edges
	}
}
