package grpc

import (
	"context"
	"fmt"
	"sync"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// bootstrapAcquirer price-checks the cheapest shipyard, then buys through BatchPurchaseShips.
type bootstrapAcquirer struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo waypointTraitLister
	savedYards   savedYardReader

	// lastAsks caches the most recent priced reading per player+ship type so a cold yard is answered
	// without a store read. It is an accelerator over savedYards, never the record itself — it starts
	// empty in every process. One instance backs the probe, hauler and gate-worker acquirers and serves
	// every player, hence the mutex.
	lastAskMu sync.Mutex
	lastAsks  map[askKey]int64
}

// waypointTraitLister lists a system's waypoints carrying a trait — the shipyard search the price-check
// walks.
type waypointTraitLister interface {
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
}

// savedYardReader is the persisted, era-scoped shipyard inventory every scanned yard writes: the durable
// record of what a yard charged, cheapest first. Era scoping is the correct forgetting — a universe reset
// retires the old asks along with the old universe.
type savedYardReader interface {
	ListSavedYards(ctx context.Context, playerID int, shipTypes []string) ([]domainShipyard.ShipTypeAvailability, error)
}

type askKey struct {
	playerID int
	shipType string
}

func (a *bootstrapAcquirer) rememberAsk(playerID int, shipType string, price int64) {
	a.lastAskMu.Lock()
	defer a.lastAskMu.Unlock()
	if a.lastAsks == nil {
		a.lastAsks = map[askKey]int64{}
	}
	a.lastAsks[askKey{playerID, shipType}] = price
}

// lastAsk reports what a yard last charged for shipType, 0 only when none ever has. The cache answers
// first; the persisted inventory answers whenever it cannot, so a reading outlives the process that took
// it (RULINGS #2). That distinction is load-bearing: callers read 0 as "no yard has ever priced this, so
// there is no evidence to act on", and a reading that died with a process would read as an absence of
// yards rather than an absence of memory.
func (a *bootstrapAcquirer) lastAsk(ctx context.Context, playerID int, shipType string) int64 {
	if cached := a.cachedAsk(playerID, shipType); cached > 0 {
		return cached
	}
	return a.savedAsk(ctx, playerID, shipType)
}

func (a *bootstrapAcquirer) cachedAsk(playerID int, shipType string) int64 {
	a.lastAskMu.Lock()
	defer a.lastAskMu.Unlock()
	return a.lastAsks[askKey{playerID, shipType}]
}

// savedAsk returns the cheapest ask on record for shipType — the same cheapest-reachable-yard reading
// PriceCheck computes live. Rows arrive price-ascending, and a 0 price marks a type that was listed but
// carried no priced listing at scan time, so the first POSITIVE price is the cheapest real ask.
func (a *bootstrapAcquirer) savedAsk(ctx context.Context, playerID int, shipType string) int64 {
	if a.savedYards == nil {
		return 0
	}
	rows, err := a.savedYards.ListSavedYards(ctx, playerID, []string{shipType})
	if err != nil {
		common.LoggerFromContext(ctx).Log("ERROR", fmt.Sprintf("Bootstrap could not read the %s asks on record — this tick weighs no yard evidence: %v", shipType, err), map[string]interface{}{
			"action":    "bootstrap_saved_ask_read_error",
			"player_id": playerID,
			"ship_type": shipType,
		})
		return 0
	}
	for _, row := range rows {
		if row.PurchasePrice > 0 {
			return int64(row.PurchasePrice)
		}
	}
	return 0
}

// PriceCheck finds the cheapest priced listing for shipType at a SHIPYARD-trait waypoint in a system
// where the player operates. readable=false (capital gate fails closed) when no priced listing is
// found — a yard prices its hulls only while a ship is standing at it, so an unvisited one reads cold.
// An unreadable read returns the LAST ask this yard gave for shipType (0 when it never has): the only
// evidence available while the yard is cold, and evidence for policy alone — every buy path gates on
// readable before it spends.
func (a *bootstrapAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return a.lastAsk(ctx, playerID, shipType), "", false, nil
	}
	ships, err := a.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return a.lastAsk(ctx, playerID, shipType), "", false, nil
	}
	systems := map[string]struct{}{}
	for _, s := range ships {
		if loc := s.CurrentLocation(); loc != nil {
			systems[shared.ExtractSystemSymbol(loc.Symbol)] = struct{}{}
		}
	}
	var cheapest int64
	var cheapestYard string
	for system := range systems {
		waypoints, werr := a.waypointRepo.ListBySystemWithTrait(ctx, system, shipyardTrait)
		if werr != nil {
			continue
		}
		for _, wp := range waypoints {
			if wp == nil {
				continue
			}
			price, ok := a.priceAtShipyard(ctx, system, wp.Symbol, shipType, pid)
			if !ok {
				continue
			}
			if cheapestYard == "" || price < cheapest {
				cheapest, cheapestYard = price, wp.Symbol
			}
		}
	}
	if cheapestYard == "" {
		return a.lastAsk(ctx, playerID, shipType), "", false, nil
	}
	a.rememberAsk(playerID, shipType, cheapest)
	return cheapest, cheapestYard, true, nil
}

func (a *bootstrapAcquirer) priceAtShipyard(ctx context.Context, system, waypoint, shipType string, pid shared.PlayerID) (int64, bool) {
	// The bootstrap capital gate spends against the winning price this search
	// returns, with no later re-verification, so the read stays Earning: metered,
	// never denied (RULINGS #4).
	//
	// NOTE FOR THE NEXT READER: this is called in a LOOP over every SHIPYARD-trait
	// waypoint in every system the fleet occupies (see PriceCheck), so one call
	// costs one undeniable live read PER YARD, and two of PriceCheck's own callers
	// discard the result entirely — they are documented read-only. That residual
	// is the largest remaining Earning-class consumer of the shipyard allowance;
	// it is left alone here because fixing it means changing WHERE the capital
	// gate gets its price (rank on the store, then take ONE live read at the
	// winner), which needs its own money-guard analysis. It is now MEASURABLE:
	// scan_budget_decisions_total{budget="shipyard",class="earning"}.
	q := &shipyardQueries.GetShipyardListingsQuery{SystemSymbol: system, WaypointSymbol: waypoint, PlayerID: pid, Class: marketscan.Earning}
	resp, err := a.med.Send(ctx, q)
	if err != nil {
		return 0, false
	}
	out, ok := resp.(*shipyardQueries.GetShipyardListingsResponse)
	if !ok || out == nil {
		return 0, false
	}
	if listing, found := out.Shipyard.FindListingByType(shipType); found {
		return int64(listing.PurchasePrice), true
	}
	return 0, false
}

// Buy purchases ONE shipType at yard through the money-integrity batch path (which navigates an idle
// hull to the yard and enforces the sp-e7je type guard). Probes are scouts — no dedicated-fleet tag.
// It picks ANY idle hull as the purchaser (the probe-buy default, where the frigate/probes are idle).
func (a *bootstrapAcquirer) Buy(ctx context.Context, playerID int, shipType, yard string) (bootstrapCmd.BuyResult, error) {
	return a.buyWith(ctx, playerID, shipType, yard, "")
}

// buyWith purchases ONE shipType at yard using `purchaser` as the purchasing hull. purchaser=="" keeps
// the legacy behavior (scan for any idle hull); a set value PINS the purchaser — the first-hauler
// pivot and every subsequent cold-start buy pass the exclusive purchasing frigate, so the buy is
// deterministic rather than dependent on an incidentally-idle hull. The batch path still enforces the
// sp-e7je money-integrity type guard and navigates the purchaser to the yard.
func (a *bootstrapAcquirer) buyWith(ctx context.Context, playerID int, shipType, yard, purchaser string) (bootstrapCmd.BuyResult, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	// One roster read serves BOTH the search and the ownership guard below. Taking it
	// only on the search path leaves a NAMED purchaser checked against nothing.
	ships, err := a.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	// Non-empty only for the yard sentinel — the one purchaser the ownership gate must not judge.
	sentinel := ""
	if purchaser == "" {
		// PREFER the exclusive purchasing ship (the pivoted command frigate) when it is idle, so
		// every cold-start + scaling buy runs through the deterministic, protected buy ship rather than an
		// incidentally-idle hull. Fall back to any idle hull before the pivot exists (e.g. the probe
		// buy) or if the purchasing ship is momentarily busy.
		for _, s := range ships {
			if s.IsIdle() && s.DedicatedFleet() == navigation.PurchasingFleet {
				purchaser = s.ShipSymbol()
				break
			}
		}
		// Then the yard sentinel, but ONLY while already docked at THIS yard — ahead of the idle
		// search because a hull at the counter buys with no flight, no fuel and nothing interrupted.
		if purchaser == "" {
			sentinel = yardSentinelAtYard(ships, yard)
			purchaser = sentinel
		}
		if purchaser == "" {
			for _, s := range ships {
				if s.IsIdle() {
					purchaser = s.ShipSymbol()
					break
				}
			}
		}
		if purchaser == "" {
			return bootstrapCmd.BuyResult{}, fmt.Errorf("no idle hull available to execute the purchase")
		}
	}

	// OWNERSHIP GATE (RULINGS #3 single-writer, #7 "do not code around ... the claim tx").
	// The buy below runs IN-PROCESS through the mediator — BatchPurchaseShipsCommand ->
	// PurchaseShipCommand -> NavigateRouteCommand/DockShipCommand — and NOT ONE of those
	// handlers consults the ship claim. Without this gate a buy issued against a hull
	// another container is actively running flies it to a shipyard and docks it out from
	// under the live worker, and nothing anywhere reports it: the loud "already assigned"
	// collision the CONTAINER claim path raises has a silent twin on this path, and a
	// silent one never appears in any failure count.
	//
	// The search path above already refuses a held hull (it only takes s.IsIdle(), which is
	// false for anything ClaimShip'd — AssignToContainer makes the hull IsAssigned). The gap
	// was the NAMED purchaser, which skipped the search and every check in it. Applied to
	// BOTH paths so a future caller cannot reopen the hole, and it is a pure REFUSAL: it can
	// only prevent a spend, never enable one, so no money guard is loosened (RULINGS #4).
	//
	// Fails CLOSED on a purchaser absent from the roster: ownership that cannot be read
	// cannot be cleared, and a hull we cannot see is one we must not fly.
	//
	// The yard sentinel is the one exemption: the assignment the gate would read is bootstrap's OWN
	// captain reservation, and the hull is already at the target yard, so the buy issues no navigate and
	// no dock. yardSentinelAtYard's reason + same-waypoint match keeps every other held hull out.
	if sentinel == "" {
		if err := ownedByAnotherContainer(ships, purchaser); err != nil {
			return bootstrapCmd.BuyResult{}, err
		}
	}

	resp, err := a.med.Send(ctx, &shipyardCmd.BatchPurchaseShipsCommand{
		PurchasingShipSymbol: purchaser,
		ShipType:             shipType,
		Quantity:             1,
		MaxBudget:            0,
		PlayerID:             pid,
		ShipyardWaypoint:     yard,
	})
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	batch, ok := resp.(*shipyardCmd.BatchPurchaseShipsResponse)
	if !ok || batch.ShipsPurchasedCount == 0 || len(batch.PurchasedShips) == 0 {
		return bootstrapCmd.BuyResult{}, fmt.Errorf("purchase returned no ship")
	}
	bought := batch.PurchasedShips[0]
	return bootstrapCmd.BuyResult{ShipSymbol: bought.ShipSymbol(), Price: int64(batch.TotalCost)}, nil
}

// yardSentinelAtYard names the yard sentinel when it is DOCKED at exactly `yard`, and "" otherwise — a
// different yard, in orbit or transit, an unresolved yard (auto-discovery), or any other reserved hull.
// SAME-WAYPOINT ONLY: the sentinel is never sent anywhere to execute a purchase.
func yardSentinelAtYard(ships []*navigation.Ship, yard string) string {
	if yard == "" {
		return ""
	}
	for _, s := range ships {
		if s == nil || !isYardSentinelShip(s) || !s.IsDocked() {
			continue
		}
		if loc := s.CurrentLocation(); loc != nil && loc.Symbol == yard {
			return s.ShipSymbol()
		}
	}
	return ""
}

// ownedByAnotherContainer reports the buy-blocking reason when `purchaser` is not the
// bootstrap's to fly: a hull carrying a live container claim (a running workflow, or a
// captain reservation — both are ACTIVE assignments) has another writer, and a hull the
// roster does not carry cannot be shown to be free. nil means the hull is unowned and the
// buy may proceed. Read-only: it never releases, never clobbers, and never retries — a
// contested hull simply does not get bought with this tick, and the next tick re-derives
// the answer from durable state (RULINGS #2).
func ownedByAnotherContainer(ships []*navigation.Ship, purchaser string) error {
	for _, s := range ships {
		if s == nil || s.ShipSymbol() != purchaser {
			continue
		}
		if s.IsAssigned() {
			return fmt.Errorf("purchaser %s is assigned to container %q — refusing to fly a hull another writer owns", purchaser, s.ContainerID())
		}
		return nil
	}
	return fmt.Errorf("purchaser %s is not in the fleet roster — refusing to fly a hull whose ownership cannot be read", purchaser)
}

// bootstrapHaulerAcquirer embeds the probe acquirer to reuse its cheapest-yard PriceCheck and the
// money-integrity BatchPurchaseShips buy (both asset-agnostic, parameterised by shipType); it only adds
// the contract-fleet dedication + hub placement that distinguish a positioned contract hauler from a
// free scout. Building nothing new (spec §Reuse).
type bootstrapHaulerAcquirer struct {
	*bootstrapAcquirer
}

// BuyAndPlace buys ONE hauler at yard (reused batch path), dedicates it to the contract fleet so the
// contract coordinator's dedicated pool adopts it (and, being the first tagged hull, seals the pool in
// exclusive mode — dropping the untagged frigate), then navigates it to its hub. The dedication uses
// the single fleet-assign write path (shipRepo.AssignFleet); placement reuses the high-level
// NavigateRouteCommand (route/refuel/flight-mode handled, idempotent if already there).
func (a *bootstrapHaulerAcquirer) BuyAndPlace(ctx context.Context, playerID int, shipType, yard, hubWaypoint, purchaserSymbol string) (bootstrapCmd.BuyResult, error) {
	bought, err := a.buyWith(ctx, playerID, shipType, yard, purchaserSymbol)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bought, err
	}
	// Dedicate to the contract fleet — the tag is what makes batch-contract's dedicated pool adopt it.
	if derr := a.shipRepo.AssignFleet(ctx, bought.ShipSymbol, contractFleetTag, pid); derr != nil {
		return bought, fmt.Errorf("dedicate hauler %s to contract fleet: %w", bought.ShipSymbol, derr)
	}
	// Place it on its hub. A nav miss is surfaced (the hull is bought + dedicated; a later tick's
	// batch-contract still adopts it wherever it is — placement is an optimisation, not a correctness bar).
	if _, nerr := a.med.Send(ctx, &navCmd.NavigateRouteCommand{ShipSymbol: bought.ShipSymbol, Destination: hubWaypoint, PlayerID: pid}); nerr != nil {
		return bought, fmt.Errorf("navigate hauler %s to hub %s: %w", bought.ShipSymbol, hubWaypoint, nerr)
	}
	return bought, nil
}

// BuyAndDedicate buys ONE hull at yard (reused batch path) and dedicates it to the arbitrary fleet tag
// `fleet` — the fleet-parameterized sibling of BuyAndPlace, minus the hub placement. The
// hull-routing trade-seed calls it with fleet="trade" to make acquisition #2 a trade hull; the dedication
// uses the SAME single fleet-assign write path (shipRepo.AssignFleet) BuyAndPlace uses over the SAME
// money-integrity BatchPurchaseShips buy, so no money guard is touched (RULINGS #4). NO navigate: a trade
// hull runs the continuous tours the trade-fleet coordinator assigns, not a fixed contract hub.
func (a *bootstrapHaulerAcquirer) BuyAndDedicate(ctx context.Context, playerID int, shipType, yard, fleet, purchaserSymbol string) (bootstrapCmd.BuyResult, error) {
	bought, err := a.buyWith(ctx, playerID, shipType, yard, purchaserSymbol)
	if err != nil {
		return bootstrapCmd.BuyResult{}, err
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return bought, err
	}
	if derr := a.shipRepo.AssignFleet(ctx, bought.ShipSymbol, fleet, pid); derr != nil {
		return bought, fmt.Errorf("dedicate hull %s to %q fleet: %w", bought.ShipSymbol, fleet, derr)
	}
	return bought, nil
}

// bootstrapShipyardScanner sends a hull to a home yard so a cold shipyard price becomes readable.
type bootstrapShipyardScanner struct {
	med          common.Mediator
	shipRepo     navigation.ShipRepository
	waypointRepo waypointTraitLister
	// savedYards is the same shipyard-inventory record bootstrapAcquirer.savedYards reads, so a
	// candidate confirmed to sell shipType can be preferred over one confirmed not to. Nil-safe:
	// unset degrades to picking the first candidate.
	savedYards savedYardReader
}

// EnsureShipyardReadable sends a hull toward a home-system SHIPYARD waypoint that can plausibly sell
// shipType, so the next tick's live GetShipyard (bootstrapAcquirer.PriceCheck) returns a priced listing.
// The listing is PRESENCE-GATED, so on a fresh universe the price stays unreadable until something
// visits a yard that sells shipType. Reuses NavigateRouteCommand; presence alone is enough to price.
//
// selectCandidateYard picks which candidate. Idempotent + best-effort: a hull already standing at a
// VIABLE yard or already IN_TRANSIT means nothing to do this tick; exhausted reports every known
// candidate confirmed not to sell shipType. Never buys, never weakens the price guard.
func (s *bootstrapShipyardScanner) EnsureShipyardReadable(ctx context.Context, playerID int, homeSystem, shipType, purchaser, borrow string) (bool, bool, error) {
	if homeSystem == "" {
		return false, false, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return false, false, nil
	}
	candidates := yardCandidates(ctx, s.waypointRepo, homeSystem)
	if len(candidates) == 0 {
		return false, false, nil // no known home-system shipyard yet — retry once waypoint data arrives
	}

	dest, isYard, exhausted := selectCandidateYard(ctx, s.savedYards, playerID, shipType, candidates)
	if exhausted {
		return false, true, nil
	}

	ships, serr := s.shipRepo.FindAllByPlayer(ctx, pid)
	if serr != nil {
		return false, false, nil
	}
	send, ok := hullToSend(ships, isYard, purchaser, borrow)
	if !ok {
		return false, false, nil
	}

	if _, nerr := s.med.Send(ctx, &navCmd.NavigateRouteCommand{ShipSymbol: send, Destination: dest, PlayerID: pid}); nerr != nil {
		return false, false, fmt.Errorf("navigate %s to home shipyard %s: %w", send, dest, nerr)
	}
	return true, false, nil
}

// yardCandidates lists a system's SHIPYARD-trait waypoint symbols, shared by EnsureShipyardReadable and
// the yard sentinel's EnsureParked so both agree on the candidate set. A read error swallows to nil,
// which every caller already treats as "retry once waypoint data arrives."
func yardCandidates(ctx context.Context, lister waypointTraitLister, homeSystem string) []string {
	yardWps, err := lister.ListBySystemWithTrait(ctx, homeSystem, shipyardTrait)
	if err != nil {
		return nil
	}
	var candidates []string
	for _, wp := range yardWps {
		if wp == nil {
			continue
		}
		candidates = append(candidates, wp.Symbol)
	}
	return candidates
}

// selectCandidateYard picks which SHIPYARD-trait waypoint to warm/stand at for shipType, using the
// persisted shipyard-inventory record: prefer a candidate CONFIRMED to sell it (cheapest first, since
// ListSavedYards orders that way), else one never scanned yet (send a hull to discover it), else report
// exhausted when every candidate is confirmed wrong. Shared by EnsureShipyardReadable and the yard
// sentinel's EnsureParked so both use one implementation.
//
// The returned isYard set EXCLUDES a candidate confirmed not to sell shipType, so a hull idling there is
// redirected toward a viable one instead of being read as already in position. Nil-safe: no inventory
// reader, or a read error, degrades to the first candidate.
func selectCandidateYard(ctx context.Context, saved savedYardReader, playerID int, shipType string, candidates []string) (string, map[string]struct{}, bool) {
	allCandidates := make(map[string]struct{}, len(candidates))
	for _, wp := range candidates {
		allCandidates[wp] = struct{}{}
	}

	if saved == nil {
		return candidates[0], allCandidates, false
	}
	rows, rerr := saved.ListSavedYards(ctx, playerID, nil)
	if rerr != nil {
		common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf("Bootstrap could not read the shipyard-inventory record while choosing a %s candidate yard — proceeding on the bare candidate list alone: %v", shipType, rerr), map[string]interface{}{
			"action":    "bootstrap_shipyard_inventory_read_error",
			"ship_type": shipType,
		})
		return candidates[0], allCandidates, false
	}

	scanned := map[string]struct{}{}
	sells := map[string]struct{}{}
	for _, row := range rows {
		if _, isCandidate := allCandidates[row.WaypointSymbol]; !isCandidate {
			continue
		}
		scanned[row.WaypointSymbol] = struct{}{}
		if row.ShipType == shipType {
			sells[row.WaypointSymbol] = struct{}{}
		}
	}

	dest := "" // cheapest confirmed seller, per ListSavedYards' own ordering
	for _, row := range rows {
		if row.ShipType != shipType {
			continue
		}
		if _, ok := sells[row.WaypointSymbol]; !ok {
			continue
		}
		dest = row.WaypointSymbol
		break
	}

	viable := make(map[string]struct{}, len(candidates))
	var unscanned []string
	for _, wp := range candidates {
		_, sold := sells[wp]
		_, seen := scanned[wp]
		switch {
		case sold:
			viable[wp] = struct{}{}
		case seen: // confirmed wrong — excluded
		default:
			unscanned = append(unscanned, wp)
			viable[wp] = struct{}{}
		}
	}

	if dest != "" {
		return dest, viable, false
	}
	if len(unscanned) > 0 {
		return unscanned[0], viable, false
	}
	return "", nil, true // every candidate scanned, none sells shipType
}

// hullToSend picks the hull to send to the yard, or reports that nothing should move this tick.
//
// A NAMED purchaser is already committed to the buy, so it goes on its own account: it is sent even
// though its purchasing dedication puts it outside the free-hull search, and it goes without waiting to
// read idle — the pivot released its loop-claim moments ago and that may not have propagated yet. Only
// its own position excuses the trip: standing at a yard means the price reads next tick, and being
// mid-flight means an earlier dispatch is still under way.
//
// With no purchaser named, the search takes a genuinely FREE hull that no other controller owns
// (RULINGS #7 — the seed→sustain handoff must never double-claim): idle (IsIdle is false for a hull
// ClaimShip'd by the contract engine — AssignToContainer makes it IsAssigned), not mid-flight, and
// dedicated to no fleet, so a contract hauler or mfg worker that is momentarily idle is never poached.
// It prefers the command frigate, the natural cold-start buyer, and stands down entirely once any hull
// is already at a yard. Once every hull carries a tag that search is empty by construction, which is
// what `borrow` — the caller's named last resort, see borrowedHull — answers.
func hullToSend(ships []*navigation.Ship, isYard map[string]struct{}, purchaser, borrow string) (string, bool) {
	atYard := func(sh *navigation.Ship) bool {
		loc := sh.CurrentLocation()
		if loc == nil {
			return false
		}
		_, ok := isYard[loc.Symbol]
		return ok && !sh.IsInTransit()
	}

	if purchaser != "" {
		for _, sh := range ships {
			if sh.ShipSymbol() != purchaser {
				continue
			}
			if atYard(sh) || sh.IsInTransit() {
				return "", false
			}
			// OWNERSHIP GATE (RULINGS #3 single-writer, #7 claim tx). Being committed to a
			// buy excuses this hull from the FREE-hull search above; it does not make it
			// ours to fly. A live container claim means another writer is running it, and
			// this navigate leg — like the buy it precedes — consults no claim of its own,
			// so without this the daemon flies a hull out from under a running workflow and
			// reports nothing. Declining costs nothing: EnsureShipyardReadable is
			// idempotent and best-effort, so the trip simply happens on a later tick once
			// the claim clears (RULINGS #2 — re-derived, never remembered).
			if sh.IsAssigned() {
				return "", false
			}
			return purchaser, true
		}
		// Fail CLOSED on a purchaser the roster does not carry: ownership that cannot be
		// read cannot be cleared.
		return "", false
	}

	var free *navigation.Ship
	for _, sh := range ships {
		if atYard(sh) {
			return "", false
		}
		if !sh.IsIdle() || sh.IsInTransit() || sh.DedicatedFleet() != "" {
			continue
		}
		// The command frigate outranks any other free hull already found.
		if free == nil || sh.Role() == commandRole {
			free = sh
		}
	}
	if free == nil {
		return borrowedHull(ships, borrow)
	}
	return free.ShipSymbol(), true
}

// borrowedHull lends the caller's named hull for the yard trip, and only while the LIVE roster still
// shows it free: idle (so no container claim or captain reservation is being flown out from under —
// RULINGS #3/#7) and not mid-flight. That re-read is the point — the caller observed it free a moment
// ago, and a hull put back on tour since must not be redirected (PLAYBOOK §9). The lend re-tags
// nothing, so the hull never leaves its fleet; it fails CLOSED on a hull the roster does not carry.
func borrowedHull(ships []*navigation.Ship, borrow string) (string, bool) {
	if borrow == "" {
		return "", false
	}
	for _, sh := range ships {
		if sh == nil || sh.ShipSymbol() != borrow {
			continue
		}
		if !sh.IsIdle() || sh.IsInTransit() {
			return "", false
		}
		return borrow, true
	}
	return "", false
}
