package parkedsensing

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ---- SeedCommandPort --------------------------------------------------------

// seedChartAPI is the slice of the SpaceTraders API a charting seed needs: chart
// the waypoint under the hull, then read back what that revealed. Narrowed to
// two methods so the port is testable without an API client.
type seedChartAPI interface {
	CreateChart(ctx context.Context, shipSymbol, token string) (*domainPorts.ChartResult, error)
	GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.WaypointDetail, error)
	// ListWaypoints reads one page of a system's waypoint list — the sweep that
	// turns a system we have never visited into one we know the shape of.
	ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*domainSystem.WaypointsListResponse, error)
}

// waypointCacheWriter persists a freshly-read waypoint. *persistence.GormWaypointRepository
// satisfies it.
type waypointCacheWriter interface {
	UpsertFromDetail(ctx context.Context, detail *domainPorts.WaypointDetail) error
}

// playerTokenReader resolves the player's API token. Narrowed to the one lookup
// the charting verbs need, so this adapter cannot reach the rest of the player
// repository — it has no business creating or updating one.
type playerTokenReader interface {
	FindByID(ctx context.Context, playerID shared.PlayerID) (*player.Player, error)
}

// SeedCommandPort drives one charting seed: the gate hop out, the hops between
// waypoints, the chart itself, and the two reads that turn a charted waypoint
// into something the screen can judge.
//
// Every call is tagged SourceCharting and NOTHING sets a priority. That
// asymmetry against the scan port next door is deliberate: charting is bounded
// work with a hull idling until it completes, so it competes for a rate-limit
// token on equal terms, while a parked scan has unbounded appetite and no
// deadline and is the one class that yields to everyone.
type SeedCommandPort struct {
	mediator  common.Mediator
	api       seedChartAPI
	players   playerTokenReader
	waypoints waypointCacheWriter
	scanner   marketScanAPI
	// neighbours is the stored gate adjacency the crossing picks its next system
	// from — the SAME store, read the same direction, that the placement mover
	// walks and that seed staging measures reach against. A nil store names no
	// next system, so the crossing fails closed without moving anything.
	neighbours appSensing.GateNeighbours
}

// NewSeedCommandPort wires the charting-seed verbs.
//
// neighbours may be nil, in which case every gate crossing fails closed rather
// than guessing — the same contract NewMoverPort's has, and for the same reason:
// a hull flown toward a gate it has no route out of is fuel spent to put it
// further from anywhere useful.
func NewSeedCommandPort(
	mediator common.Mediator,
	api seedChartAPI,
	players playerTokenReader,
	waypoints waypointCacheWriter,
	scanner marketScanAPI,
	neighbours appSensing.GateNeighbours,
) *SeedCommandPort {
	return &SeedCommandPort{
		mediator: mediator, api: api, players: players,
		waypoints: waypoints, scanner: scanner, neighbours: neighbours,
	}
}

// JumpTo advances a hull ONE step of its gate CROSSING to targetSystem: the
// in-system move onto the gate, or the jump off it. It returns either way.
//
// TARGETSYSTEM IS A DESTINATION, NOT A NEIGHBOUR. Naming it as the jump's
// destination directly is correct only while a seed's errand is a system next
// door. Seed staging reaches across the whole traversable component, so the
// errand's target is routinely NOT connected to the gate the hull is standing
// on, and a jump naming an
// unconnected system is rejected by the API: the hull would sit on the gate
// re-issuing a refused command every
// tick, indistinguishable from a reactor cooldown, charting nothing while
// holding probe-cap headroom.
//
// So the crossing is WALKED, through exactly the search and the step the
// placement mover uses. The next system is resolved from stored adjacency BEFORE
// anything moves, which is what keeps an unroutable errand from buying a wasted
// flight to a gate, and the hop is dispatched and returned from. Nothing about
// how far the crossing has got is persisted here and nothing needs to be: the
// seed's errand row names the target and the ships table names where the hull
// stands, so the next tick re-reads both and re-derives the next hop from where
// it ACTUALLY is — which is what makes the walk resume across a restart and
// self-correct when a hull ends up somewhere the last step did not send it.
//
// WHY THE HOP IS SPLIT. A gate crossing is two physical moves, and only the first
// is a flight. JumpShipCommand does both — it finds the nearest gate, flies the
// hull there with NavigateRouteCommand, and only then jumps — so sending it from
// off-gate waits out that flight inside the tick. That is the same defect as the
// placement mover's, one layer in, and it lands on the stage this engine exists
// for: expansion is where charting seeds launch, so a seed blocking here stops
// the fleet discovering new systems at all.
//
// Split, each step is a command that returns, and the ships table carries the
// hull between them exactly as it does everywhere else in this engine: the hop is
// dispatched, ShipStateScheduler records the landing, and the NEXT tick reads a
// hull standing on the gate and jumps it. The seed's own machine already skips a
// hull the ships table reports IN_TRANSIT, so the intervening ticks cost nothing.
//
// THE DISCRIMINATOR IS THE WAYPOINT, NEVER THE DISTANCE. fromWaypoint is compared
// to the gate symbol, because orbitals share coordinates with the body they orbit
// — a hull can sit at zero distance from a gate it is not standing on, and
// jumping "from" there would put us straight back on the blocking branch.
//
// The jump itself needs no wait: it is instantaneous at the API, leaving only a
// reactor cooldown, which gates the next JUMP rather than the navigate that
// follows it. That cooldown is NOT re-learned from the API: stepThroughGate reads
// it off the hull's own row and holds the hop without a call until it clears, so a
// cooling seed costs this walk nothing but a database read.
func (p *SeedCommandPort) JumpTo(ctx context.Context, playerID int, shipSymbol, fromWaypoint, targetSystem string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	currentSystem := shared.ExtractSystemSymbol(fromWaypoint)
	if currentSystem == targetSystem {
		// Already arrived. Defensive — dispatchSeed reads the position and hands
		// the errand to its arrival branch in this case — but it costs nothing
		// and keeps the verb correct on its own.
		return nil
	}

	// Resolved before any movement, so an errand the stored graph cannot route never
	// buys a wasted flight to a gate. Reaching this branch means the hull now stands
	// somewhere whose OWN adjacency we do not hold — routine, since the edge that
	// carried it in was charted from the far end. PUBLISHED THROUGH THE SENTINEL: a
	// definite store read, not a call that failed, so the engine can end the errand.
	nextSystem, err := nextHopToward(ctx, p.neighbours, currentSystem, targetSystem)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Charting seed %s is bound for %s from %s, but an unbounded search of stored adjacency found no connected path: %v",
			shipSymbol, targetSystem, currentSystem, err), map[string]interface{}{
			"action":        "parked_sensing_seed_walk_unroutable",
			"ship_symbol":   shipSymbol,
			"from_system":   currentSystem,
			"target_system": targetSystem,
		})
		return fmt.Errorf("failed to name the next system for seed %s bound for %s: %w: %w",
			shipSymbol, targetSystem, appSensing.ErrSeedWalkUnroutable, err)
	}
	return stepThroughGate(ctx, p.mediator, pid, shipSymbol, fromWaypoint, nextSystem)
}

// gateToLeaveFrom names the jump gate in the hull's CURRENT system that a hop
// leaves from. Reuses the same query JumpShipCommand uses to pick one, so the
// gate this port moves the hull to is by construction the gate that command
// would have chosen — there is no window in which the two disagree and the hull
// is flown to one gate and jumped from another.
//
// Shared by BOTH gate walkers — the charting seed's hop and the placement
// mover's — for the same reason dispatchHop is: they answer the same question,
// and two implementations of it could drift into moving a hull to one gate and
// jumping it from another.
func gateToLeaveFrom(ctx context.Context, med common.Mediator, playerID int, shipSymbol string) (string, error) {
	res, err := med.Send(ctx, &shipQueries.FindNearestJumpGateQuery{
		ShipSymbol: shipSymbol,
		PlayerID:   &playerID,
	})
	if err != nil {
		return "", fmt.Errorf("failed to find the jump gate %s leaves from: %w", shipSymbol, err)
	}
	resp, ok := res.(*shipQueries.FindNearestJumpGateResponse)
	if !ok || resp.JumpGate == nil || resp.JumpGate.Symbol == "" {
		// Fail closed: never move a hull toward a gate we cannot name. The
		// errand holds and the next tick asks again.
		return "", fmt.Errorf("no jump gate could be named for %s", shipSymbol)
	}
	return resp.JumpGate.Symbol, nil
}

// NavigateTo dispatches a hull to a waypoint inside the system it is already in
// and returns as soon as the API has accepted the move.
//
// DISPATCH, NOT JOURNEY, and SeedCommander says so in its own words: "every
// method is a single command with no retry and no waiting: the tick issues one
// and returns, and the next tick reads the ships table to see what happened".
// The pass that drives this already opens by skipping any seed the ships table
// reports IN_TRANSIT ("already flying, under this or an earlier tick's
// command"), so waiting here buys nothing at all — it only holds the tick open
// for a leg of the tour, starving the rest of the tick behind it.
//
// Shares dispatchHop with the placement mover, including its orbit-first step:
// a seed charts from a berth, so it is docked at the waypoint it just finished.
func (p *SeedCommandPort) NavigateTo(ctx context.Context, playerID int, shipSymbol, waypoint string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return dispatchHop(ctx, p.mediator, pid, shipSymbol, waypoint)
}

// Dock berths a hull where it stands, through the same command the placement mover
// uses. Berthing an already-docked hull is a benign no-op.
func (p *SeedCommandPort) Dock(ctx context.Context, playerID int, shipSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	if _, err := p.mediator.Send(sensingCtx(ctx), &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   pid,
	}); err != nil {
		return fmt.Errorf("failed to dock %s: %w", shipSymbol, err)
	}
	return nil
}

// Chart publicly charts the waypoint the hull is standing on.
//
// A waypoint somebody else has already charted answers 4230, and that is
// SUCCESS, not failure: the waypoint is public, which is the entire outcome the
// call was after. Swallowing it here (rather than letting the engine reason
// about API codes) is what keeps a frontier another agent got to first from
// stalling a tour on an error it can do nothing about.
//
// A fresh chart PAYS, so the reward is recorded before this returns: the credits are
// already in the balance, and an unrecorded inflow leaves a gap in the ledger chain.
func (p *SeedCommandPort) Chart(ctx context.Context, playerID int, shipSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	token, err := p.token(ctx, playerID)
	if err != nil {
		return err
	}
	result, err := p.api.CreateChart(sensingCtx(ctx), shipSymbol, token)
	if err != nil {
		if !isAlreadyCharted(err) {
			return fmt.Errorf("failed to chart the waypoint under %s: %w", shipSymbol, err)
		}
		return nil // already public: no chart was made, so no reward was paid
	}
	ledgerCommands.RecordChartReward(ctx, p.mediator, pid, shipSymbol, result)
	return nil
}

// RefreshWaypoint re-reads a charted waypoint and writes it back to the cache,
// reporting whether it carries a marketplace.
//
// The persist is the point. A waypoint stays UNCHARTED in the cache until this
// row is rewritten, and the tour picks its next stop from exactly that set — so
// without this the seed would chart the same waypoint on every tick forever.
// The read is therefore only reported as a success once the write has landed.
func (p *SeedCommandPort) RefreshWaypoint(ctx context.Context, playerID int, system, waypoint string) (bool, error) {
	token, err := p.token(ctx, playerID)
	if err != nil {
		return false, err
	}
	detail, err := p.api.GetWaypoint(sensingCtx(ctx), system, waypoint, token)
	if err != nil {
		return false, fmt.Errorf("failed to re-read waypoint %s: %w", waypoint, err)
	}
	if err := p.waypoints.UpsertFromDetail(ctx, detail); err != nil {
		return false, fmt.Errorf("failed to persist the charted waypoint %s: %w", waypoint, err)
	}
	for _, trait := range detail.Traits {
		if trait == marketplaceTrait {
			return true, nil
		}
	}
	return false, nil
}

// SyncWaypoints sweeps a system's whole waypoint list and persists every row.
//
// This is what turns "we have never been here" into a catalog the screen and the
// charting tour can both read. It is the ONLY method on this port that issues
// more than one API call — the list is paginated — and the pages are walked here
// rather than by the engine so a partially-swept catalog is never visible: the
// tour picks its next stop from the stored uncharted set, and half a catalog
// reads as a system that is nearly finished.
//
// A page that fails aborts the sweep. The caller does not stamp the catalog as
// synced unless this returns cleanly, so a torn sweep is simply retried.
func (p *SeedCommandPort) SyncWaypoints(ctx context.Context, playerID int, system string) error {
	token, err := p.token(ctx, playerID)
	if err != nil {
		return err
	}
	ctx = sensingCtx(ctx)

	for page := 1; page <= maxCatalogPages; page++ {
		listing, err := p.api.ListWaypoints(ctx, system, token, page, catalogPageSize)
		if err != nil {
			return fmt.Errorf("failed to sweep the waypoint catalog of %s at page %d: %w", system, page, err)
		}
		for _, waypoint := range listing.Data {
			detail := &domainPorts.WaypointDetail{
				Symbol:   waypoint.Symbol,
				Type:     waypoint.Type,
				X:        waypoint.X,
				Y:        waypoint.Y,
				Traits:   traitSymbols(waypoint.Traits),
				Orbitals: orbitalSymbols(waypoint.Orbitals),
			}
			if err := p.waypoints.UpsertFromDetail(ctx, detail); err != nil {
				return fmt.Errorf("failed to persist swept waypoint %s: %w", waypoint.Symbol, err)
			}
		}
		if len(listing.Data) < catalogPageSize {
			return nil
		}
	}
	// Ran out of pages to walk. Reported as an error rather than a silent
	// truncation: the caller must not stamp a catalog it only partly has.
	return fmt.Errorf("waypoint catalog of %s exceeds %d pages; sweep abandoned", system, maxCatalogPages)
}

// traitSymbols flattens the API's trait objects to their symbols.
func traitSymbols(traits []map[string]interface{}) []string {
	out := make([]string, 0, len(traits))
	for _, trait := range traits {
		if symbol, ok := trait["symbol"].(string); ok {
			out = append(out, symbol)
		}
	}
	return out
}

// orbitalSymbols flattens the API's orbital objects to their symbols.
func orbitalSymbols(orbitals []map[string]string) []string {
	out := make([]string, 0, len(orbitals))
	for _, orbital := range orbitals {
		if symbol := orbital["symbol"]; symbol != "" {
			out = append(out, symbol)
		}
	}
	return out
}

// ReadMarketAt scans the market the hull is standing at and persists its prices
// and price history, through the same scanner the rest of the fleet uses.
//
// The scan outcome is deliberately discarded. This path keeps no freshness
// ledger — it is a seed reading the market under its feet on the way past — so a
// budget decline is simply a read served from the store, with nothing anywhere
// claiming otherwise. The scan pacer is the caller that must NOT discard it; see
// ScanRunnerPort.Run.
func (p *SeedCommandPort) ReadMarketAt(ctx context.Context, playerID int, waypoint string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	_, err = p.scanner.ScanAndSaveMarketWithOutcome(sensingCtx(ctx), uint(pid.Value()), waypoint)
	return err
}

// token resolves the player's API token, mirroring the gate graph's own lookup.
func (p *SeedCommandPort) token(ctx context.Context, playerID int) (string, error) {
	return playerToken(ctx, p.players, playerID)
}

// playerToken is the package's single token lookup, shared by every adapter here
// that talks to the API directly. One implementation so a change in how the
// token is resolved cannot apply to some outbound calls and not others.
func playerToken(ctx context.Context, players playerTokenReader, playerID int) (string, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", err
	}
	entity, err := players.FindByID(ctx, pid)
	if err != nil {
		return "", fmt.Errorf("failed to load player %d: %w", playerID, err)
	}
	return entity.Token, nil
}

// isAlreadyCharted reports whether a CreateChart failure is the API's benign
// "waypoint already charted" verdict (HTTP 400, code 4230). Matching on the code
// and the message mirrors the gate graph's identical classifier and is robust to
// the adapter wrapping the underlying *APIError.
func isAlreadyCharted(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "4230") || strings.Contains(msg, "already charted")
}
