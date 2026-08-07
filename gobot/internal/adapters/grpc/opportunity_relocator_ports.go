package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// This file wires the opportunity relocator's driven ports (sp-zvywu Part 2) to the concrete daemon
// collaborators — the twin of fleet_autosizer_ports.go and contract_scaler_ports.go. The reconciler
// (tradingCmd.RunOpportunityRelocatorHandler) owns every decision and is unit-tested against fakes;
// these are the thin bridges the daemon injects at boot. No economics live here.
//
// FIVE of the six ports are wired here; the sixth — RelocatorRegionObserver, the only one that needs
// the trade solver — is implemented by the tour coordinator itself
// (run_tour_coordinator_relocation_regions.go), so the relocator and the two existing relocation
// triggers price a ground through ONE pre-flight.
//
// Every port is READ-ONLY except the actuator (pure movement — no buy, no sell, no money guard
// touched, RULINGS #4) and the intent store (the relocator's own container config, RULINGS #2).

const (
	// tourRunCommandType is the persisted command_type of a per-hull tour container. It is the key
	// the fleet observer distinguishes "this hull is out working a tour" from "this hull is held by
	// some OTHER operation" on — the same value briefing_source.go counts active tours by and
	// container_ops_tour.go registers the container under.
	tourRunCommandType = "tour_run"
	// relocationIntentsConfigKey is the container-config key the relocation intents round-trip
	// through: ONE JSON object keyed by ship symbol, one record per hull, mirroring how
	// TourRepositionConfigPersister keeps its in-flight reposition keys in the tour container's own
	// config.
	relocationIntentsConfigKey = "relocation_intents"
	// relocationOfferUntilConfigKey / relocationOfferBackoffConfigKey are the sp-e8d92 first-refusal keys
	// the TOUR writes into its own container config and the relocator reads back. Absolute RFC 3339
	// instants; an empty value means no offer.
	relocationOfferUntilConfigKey   = "relocation_offer_until"
	relocationOfferBackoffConfigKey = "relocation_offer_backoff_until"
)

// --- RelocatorFleetObserver: the live trade hulls with the facts RULINGS #7 is enforced on ---

// relocatorShipLister is the narrow fleet read the observer needs. Satisfied by
// navigation.ShipRepository; an interface so the observer depends on behaviour and is testable at the
// port boundary.
//
// It is FindAllByPlayer, NOT FindActiveByPlayer. That choice is load-bearing and deliberate:
// FindActiveByPlayer filters to ships where IsAssigned() is true (adapters/api/ship_repository.go),
// which is exactly the complement of what this port must find. A trade hull between tours has been
// force-released by its finished tour container, so it is IDLE and absent from the "active" set —
// meaning an observer built on FindActiveByPlayer would surface only hulls another operation is
// already holding, every one of which this observer then marks OnTour or Pinned. The reconciler would
// be permanently dormant while looking perfectly healthy. (activeTradeHullsBySystem does use
// FindActiveByPlayer, correctly: it answers a different question — which systems are OCCUPIED, for
// the anti-herd cap — where "assigned" is the right filter.) FindAllByPlayer with a
// DedicatedFleet()=="trade" filter is the existing trade-POOL seam autosizerHeavySources.HeavyCount
// reads, and it carries the same enriched assignment state (FindActiveByPlayer is a filter over it).
type relocatorShipLister interface {
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}

// relocatorContainerLister lists containers in a given status — how the observer learns which hulls
// are out on a tour. Satisfied by *persistence.ContainerRepositoryGORM.
type relocatorContainerLister interface {
	ListByStatus(ctx context.Context, status container.ContainerStatus, playerID *int) ([]*persistence.ContainerModel, error)
}

// RelocatorFleetObserverAdapter lists the player's live trade hulls with the position and protection
// facts the relocator filters on.
type RelocatorFleetObserverAdapter struct {
	ships      relocatorShipLister
	containers relocatorContainerLister
}

// NewRelocatorFleetObserver wires the relocator's fleet observation to the live ship + container
// repositories.
func NewRelocatorFleetObserver(ships relocatorShipLister, containers relocatorContainerLister) *RelocatorFleetObserverAdapter {
	return &RelocatorFleetObserverAdapter{ships: ships, containers: containers}
}

// ObserveTradeHulls reports every TRADE-dedicated hull with the three facts RULINGS #7 and the
// honest-release rule are enforced on.
//
// SCOPE: DedicatedFleet()=="trade" only. That single filter is the DEDICATION half of RULINGS #7 —
// "Pinned/dedicated hulls are never poached" — enforced by construction: a hull dedicated to
// contract / manufacturing / explorer / purchasing is never even observed, so no scoring path can
// reach it and there is nothing for a later guard to get wrong.
//
// PINNED is the OCCUPANCY half, and it matters here more than it would elsewhere because the
// relocator's actuator does NOT claim the hull — RepositionToWaypointWithinJumps trusts a claim its
// caller already holds (see the actuator below), and the relocator holds none. So an active claim is
// the ONLY thing standing between this reconciler and poaching-by-movement, and it is honoured
// exactly as the codebase's own ownership model states it:
//
//   - IsReservedByCaptain() — the operator-authority pin. Its own contract is that a reserved hull is
//     "hiding it from every coordinator's assignment discovery" (Ship.ReserveByCaptain), so a
//     coordinator that moved one anyway would be defeating the reservation rather than honouring it.
//   - IsAssigned() to a container that is NOT a tour_run — a different operation (a long-haul worker,
//     a cargo liquidation, a one-shot route) exclusively holds this hull. Flying it away mid-operation
//     is the "Do not code around fleet pins" failure with extra steps.
//
// ON TOUR is the honest-release rule, and it is kept SEPARATE from Pinned rather than folded into it
// so the heartbeat keeps telling the truth: a touring hull is expected, temporary, and counted as
// mid_tour, whereas a pinned one is a protection event. A tour claim is recognised by the hull's OWN
// container row carrying command_type=tour_run, in RUNNING or PENDING — PENDING included because a
// queued tour is about to claim the hull, and relocating into that window is the same race as
// relocating mid-tour. A hull physically IN TRANSIT is also reported OnTour: whatever put it in
// flight, it is not at honest release, which is precisely what the field means ("skipped and
// reconsidered on a later tick").
//
// FAIL CLOSED: an unreadable fleet or container surface returns an error. The relocator logs and
// counts the tick and re-derives everything next tick, so a transient blip costs one cadence and
// never a relocation decided against an unknown ownership picture.
func (o *RelocatorFleetObserverAdapter) ObserveTradeHulls(ctx context.Context, playerID int) ([]tradingCmd.RelocatorHull, error) {
	if o == nil || o.ships == nil {
		return nil, fmt.Errorf("opportunity relocator fleet observer is not wired to a ship repository")
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil, fmt.Errorf("opportunity relocator cannot observe the fleet: %w", err)
	}
	ships, err := o.ships.FindAllByPlayer(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("opportunity relocator cannot read the fleet: %w", err)
	}
	tours, err := o.tourOccupancy(ctx, playerID)
	if err != nil {
		return nil, err
	}

	hulls := make([]tradingCmd.RelocatorHull, 0, len(ships))
	for _, ship := range ships {
		if ship == nil || ship.DedicatedFleet() != tradeFleetTag {
			continue
		}
		location := ship.CurrentLocation()
		if location == nil {
			continue // a hull with no readable position cannot be relocated FROM anywhere
		}
		hulls = append(hulls, relocatorHullFrom(ship, location.SystemSymbol, tours))
	}
	return hulls, nil
}

// ObserveHull re-reads ONE hull's live protection facts for the actuation-time re-check (sp-x2jr6
// slice 1) — the guard that stops the relocator flying a hull a tour claimed while the region pre-flight
// was running.
//
// It derives every fact through relocatorHullFrom, the SAME function ObserveTradeHulls uses, so the
// commit gate and the scoring gate cannot disagree about what a protected hull is. It re-reads the
// container rows too: the whole point is that the tour row may have appeared since the snapshot, so
// reusing a cached occupancy map would make the check answer the stale question it exists to re-ask.
//
// A hull that has VANISHED from the trade fleet — sold, re-dedicated, or newly unreadable — is an
// error, not an absence. The caller fails the move closed on it, which is right: a hull that is no
// longer a trade hull is emphatically not one this reconciler may move.
func (o *RelocatorFleetObserverAdapter) ObserveHull(ctx context.Context, playerID int, shipSymbol string) (tradingCmd.RelocatorHull, error) {
	if o == nil || o.ships == nil {
		return tradingCmd.RelocatorHull{}, fmt.Errorf("opportunity relocator fleet observer is not wired to a ship repository")
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return tradingCmd.RelocatorHull{}, fmt.Errorf("opportunity relocator cannot re-read %s: %w", shipSymbol, err)
	}
	ships, err := o.ships.FindAllByPlayer(ctx, pid)
	if err != nil {
		return tradingCmd.RelocatorHull{}, fmt.Errorf("opportunity relocator cannot re-read the fleet for %s: %w", shipSymbol, err)
	}
	tours, err := o.tourOccupancy(ctx, playerID)
	if err != nil {
		return tradingCmd.RelocatorHull{}, err
	}
	for _, ship := range ships {
		if ship == nil || ship.ShipSymbol() != shipSymbol {
			continue
		}
		if ship.DedicatedFleet() != tradeFleetTag {
			return tradingCmd.RelocatorHull{}, fmt.Errorf("%s is no longer dedicated to the trade fleet (now %q), so it may not be relocated", shipSymbol, ship.DedicatedFleet())
		}
		location := ship.CurrentLocation()
		if location == nil {
			return tradingCmd.RelocatorHull{}, fmt.Errorf("%s has no readable position, so its relocation cannot be proven safe", shipSymbol)
		}
		return relocatorHullFrom(ship, location.SystemSymbol, tours), nil
	}
	return tradingCmd.RelocatorHull{}, fmt.Errorf("%s is no longer in the fleet, so its relocation cannot be proven safe", shipSymbol)
}

// relocatorHullFrom projects one ship into the relocator's view — the SINGLE derivation of the facts
// RULINGS #7 is enforced on, read by the bulk observation and by the actuation re-check alike. A second
// copy of this is how the two gates end up disagreeing about what a protected hull is.
func relocatorHullFrom(ship *navigation.Ship, systemSymbol string, tours tourOccupancy) tradingCmd.RelocatorHull {
	inTransit := ship.IsInTransit()
	onTour := inTransit || tours.holds(ship)
	// An OFFERED hull is still OnTour — its container is running, it is simply waiting — and the
	// reconciler lifts the mid-tour exclusion for it. A hull IN TRANSIT is never offered whatever the row
	// says: it is physically flying, so no offer can make it available.
	offered := !inTransit && tours.offeredHulls[ship.ShipSymbol()]
	return tradingCmd.RelocatorHull{
		ShipSymbol:       ship.ShipSymbol(),
		CurrentSystem:    systemSymbol,
		IsCommandFrigate: ship.Role() == commandRole,
		Pinned:           ship.IsReservedByCaptain() || (ship.IsAssigned() && !onTour),
		OnTour:           onTour,
		InTransit:        inTransit,
		Offered:          offered,
	}
}

// relocationOfferDeadline reads the offer deadline a tour row carries, or the zero time when the key is
// absent, empty, or unparseable. Unparseable reads as NO OFFER (fail-safe): the cost of ignoring a real
// offer is one missed relocation, while the cost of honouring a corrupt one is a hull held out of
// touring.
func relocationOfferDeadline(configJSON string) time.Time {
	if configJSON == "" {
		return time.Time{}
	}
	var config struct {
		OfferedUntil string `json:"relocation_offer_until"`
	}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil || config.OfferedUntil == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, config.OfferedUntil)
	if err != nil {
		return time.Time{}
	}
	return at.UTC()
}

// tourOccupancy is what the live container rows say about which hulls are out on a tour, in the TWO
// representations a tour's ownership takes at different points in its launch (sp-x2jr6 slice 2).
//
// They do not overlap, which is the whole reason both are needed. A tour launch INSERTs its container
// row (containerRepo.Add) and only THEN claims the hull (the runner's ClaimShip). So:
//
//	before the claim: the ROW names its hull; the hull points at nothing.
//	after  the claim: the hull points at the row; both agree.
//
// A predicate keyed only on the hull's own claim is therefore blind for the whole interval before the
// claim lands — and that interval is exactly when a racing coordinator sees a free hull.
type tourOccupancy struct {
	// containerIDs are the tour containers' own IDs, matched against a hull's claim once ClaimShip has
	// landed. This is the DERIVED representation.
	containerIDs map[string]bool
	// declaredHulls are the hulls tour rows NAME in config["ship_symbol"] — the DECLARED
	// representation, and the only evidence that exists before the claim.
	declaredHulls map[string]bool
	// offeredHulls are the hulls whose tour has DURABLY offered them for relocation and whose offer has
	// not yet lapsed (sp-e8d92 first refusal). Read from the very same rows, so first refusal costs no
	// extra query and makes no API call.
	offeredHulls map[string]bool
}

// holds reports whether a live tour row accounts for this hull, by either representation.
func (t tourOccupancy) holds(ship *navigation.Ship) bool {
	if t.declaredHulls[ship.ShipSymbol()] {
		return true
	}
	return ship.IsAssigned() && t.containerIDs[ship.ContainerID()]
}

// tourOccupancy reads the RUNNING and PENDING tour rows into both representations.
//
// A read failure is an ERROR, never an empty set: an empty set would read as "nobody is touring" and
// promote every touring hull to eligible, which is the one mistake this lookup exists to prevent.
//
// STATUS SCOPE is deliberately RUNNING + PENDING and no wider. A tour in STOPPING or INTERRUPTED still
// holding its hull is already caught by Pinned (an assigned hull under any container that is not a live
// tour reads as a protection event), so widening buys nothing — while an INTERRUPTED row that never
// recovers would exclude its hull FOREVER, which is the silent-dormancy failure this observer has
// already been bitten by once.
//
// A tour row whose config cannot be parsed, or which names no hull, contributes its container ID but no
// declared hull. That is deliberate: it keeps the CLAIMED window guarded for that row and loses only
// the pre-claim window for a tour whose launch config is already corrupt. Erroring instead would let
// one malformed row hold the entire reconciler dormant — strictly worse than a narrow unguarded window,
// and the same dormancy trap as above.
func (o *RelocatorFleetObserverAdapter) tourOccupancy(ctx context.Context, playerID int) (tourOccupancy, error) {
	if o.containers == nil {
		return tourOccupancy{}, fmt.Errorf("opportunity relocator fleet observer is not wired to a container repository, so a touring hull cannot be told from an idle one")
	}
	tours := tourOccupancy{containerIDs: map[string]bool{}, declaredHulls: map[string]bool{}, offeredHulls: map[string]bool{}}
	now := time.Now().UTC()
	for _, status := range []container.ContainerStatus{container.ContainerStatusRunning, container.ContainerStatusPending} {
		models, err := o.containers.ListByStatus(ctx, status, &playerID)
		if err != nil {
			return tourOccupancy{}, fmt.Errorf("opportunity relocator cannot read %s containers to identify touring hulls: %w", status, err)
		}
		for _, model := range models {
			if model == nil || model.CommandType != tourRunCommandType {
				continue
			}
			tours.containerIDs[model.ID] = true
			hull := declaredContainerHull(model.Config)
			if hull == "" {
				continue
			}
			tours.declaredHulls[hull] = true
			// sp-e8d92 first refusal: a tour that has reached its boundary offers its hull for
			// relocation until a deadline. THE DEADLINE IS RE-CHECKED HERE, against now, rather than
			// trusted as a boolean — a stale "offered" flag would keep a hull out of touring forever,
			// which is a stranded trade hull and strictly worse than one that never spread.
			if relocationOfferDeadline(model.Config).After(now) {
				tours.offeredHulls[hull] = true
			}
		}
	}
	return tours, nil
}

// declaredContainerHull reads the hull a container's launch config NAMES, or "" when the config is
// absent, unparseable, or names none. The key is the one container_ops_tour.go writes at launch and
// buildTourCoordinatorCommand reads back on restart, so the declaration this matches is the same one
// the tour itself is rebuilt from.
func declaredContainerHull(configJSON string) string {
	if configJSON == "" {
		return ""
	}
	var config struct {
		ShipSymbol string `json:"ship_symbol"`
	}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return ""
	}
	return config.ShipSymbol
}

// --- RelocatorTelemetryObserver: the realized per-hull tour rate's source rows ---

// relocatorTelemetryReader is the narrow telemetry read the per-hull EWMA needs. Satisfied by
// trading.TourTelemetryRepository (persistence.TourTelemetryRepositoryGORM).
type relocatorTelemetryReader interface {
	ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error)
}

// RelocatorTelemetryObserverAdapter forwards the relocator's telemetry window to the persisted
// per-leg tour telemetry — the SAME rows senseRateFloor and the placement senseBeta read, so all
// three rate-based relocation paths judge a hull on one basis.
type RelocatorTelemetryObserverAdapter struct{ telemetry relocatorTelemetryReader }

// NewRelocatorTelemetryObserver wires the relocator's telemetry read to the tour telemetry
// repository.
func NewRelocatorTelemetryObserver(telemetry relocatorTelemetryReader) *RelocatorTelemetryObserverAdapter {
	return &RelocatorTelemetryObserverAdapter{telemetry: telemetry}
}

// ObserveTourTelemetry returns the rows whose PlannedAt is at or after since. An error propagates: the
// relocator treats unreadable telemetry as "no hull has a provable rate", which relocates nothing —
// fail closed by construction rather than by a special case.
func (o *RelocatorTelemetryObserverAdapter) ObserveTourTelemetry(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error) {
	if o == nil || o.telemetry == nil {
		return nil, fmt.Errorf("opportunity relocator telemetry observer is not wired")
	}
	return o.telemetry.ListByPlayer(ctx, playerID, since)
}

// --- RelocatorEraHorizon: honestly UNKNOWN ---

// RelocatorEraHorizonAdapter reports the remaining era hours. It always reports UNKNOWN, and that is
// the correct answer today rather than a stub: THERE IS NO ERA-END DATE IN THE SCHEMA. The era row
// (persistence.EraModel) carries UniverseResetDate (when the era BEGAN), RegisteredAt, and ClosedAt —
// which is written after the fact, when the era is already over. Nothing records when the open era
// will end, so any number returned here would be invented, and an invented horizon is the input the
// endgame guard is most sensitive to: too long and the guard licenses a move the era cannot repay,
// too short and it refuses every move.
//
// Reporting UNKNOWN is SAFE, not dormant, and that is why this adapter exists rather than leaving the
// port nil. trading.ValueRelocation bounds an unknown horizon to the reconciler's own horizon knob
// (defaultRelocatorHorizonHours, 24h) and keeps valuing moves against it; the endgame guard still
// applies, just against the horizon instead of the era. So the relocator stays fully live on an
// unknowable era — the behaviour the reconciler's own
// TestOpportunityRelocatorShould_StillRelocateWhenTheEraHorizonIsUnknown pins.
//
// WHEN AN ERA-END BECOMES KNOWABLE (an operator-set expected close date, a faction-reset schedule),
// this is the ONE place to read it: return (hours, true) and the valuation immediately prefers the era
// bound wherever it binds tighter than the horizon. No scoring code changes.
type RelocatorEraHorizonAdapter struct{}

// NewRelocatorEraHorizon wires the era-horizon port.
func NewRelocatorEraHorizon() *RelocatorEraHorizonAdapter { return &RelocatorEraHorizonAdapter{} }

// RemainingEraHours reports (0, false) — unknown. See the type comment: the schema holds no era-end
// date, and the valuation bounds an unknown horizon to its horizon knob rather than going dormant.
func (r *RelocatorEraHorizonAdapter) RemainingEraHours(context.Context, int) (float64, bool) {
	return 0, false
}

// --- RelocatorActuator: pure movement, NO SPEND ---

// relocatorRepositioner is the narrow movement primitive the relocation rides. Satisfied by
// *tradingCmd.RunTradeRouteCoordinatorHandler's exported RepositionToWaypointWithinJumps — the SAME
// call the margins-death rescue (maybeReposition) and the rate-floor rescue
// (commitRateFloorRelocation) commit their jumps through, and the same one scout_reposition and
// worker_ferry declare their own narrow Repositioner interfaces for.
//
// Choosing the stored-adjacency variant over the strict one is sp-kl16's ruling, inherited unchanged:
// a relocation is a MOVEMENT of the hull, not a commitment of money, so it routes PAST an unreadable
// origin gate over the persisted topology instead of fail-closing on the strict fetch-through Path —
// which is what left a heavy unroutable off an unreadable-gate origin even where a 2-hop stored route
// existed. Every money-commitment path keeps the strict resolver.
type relocatorRepositioner interface {
	RepositionToWaypointWithinJumps(ctx context.Context, shipSymbol, destinationWaypoint string, playerID, maxJumps int) error
}

// RelocatorActuatorAdapter moves a hull to a region's landing waypoint. It is a PURE movement port:
// it never buys, sells, quotes, or reads a money guard (RULINGS #4 — the relocator's whole economics
// is travel and a re-plan on arrival).
//
// It deliberately does NOT carry the deadhead look-back manifest the tour's own relocations load
// (loadLookbackManifest): that is a BUY, and a buy needs the tour container's resolved per-tour budget
// and its purchase-obligation carry to discharge it. The relocator has neither, so a look-back load
// here would be an unattributed spend against a hull no container is holding. The hull re-plans at the
// destination and earns from there.
type RelocatorActuatorAdapter struct{ repositioner relocatorRepositioner }

// NewRelocatorActuator wires the relocation's movement to the shared cooldown-riding travel
// machinery.
func NewRelocatorActuator(repositioner relocatorRepositioner) *RelocatorActuatorAdapter {
	return &RelocatorActuatorAdapter{repositioner: repositioner}
}

// RelocateHull flies shipSymbol to targetWaypoint over the stored adjacency bounded to jumpBound. The
// call BLOCKS until arrival, which is what makes the relocator's persisted intent a genuine in-flight
// record: a crash during this call leaves the intent uncompleted and the next tick RESUMES the same
// target rather than re-deciding it.
func (a *RelocatorActuatorAdapter) RelocateHull(ctx context.Context, playerID int, shipSymbol, targetWaypoint string, jumpBound int) error {
	if a == nil || a.repositioner == nil {
		return fmt.Errorf("opportunity relocator actuator is not wired to a movement primitive")
	}
	return a.repositioner.RepositionToWaypointWithinJumps(ctx, shipSymbol, targetWaypoint, playerID, jumpBound)
}

// --- RelocationIntentStore: the restart contract's durable half (RULINGS #2) ---

// relocationIntentRecord is ONE hull's persisted relocation intent, as it sits in the container
// config. Explicit JSON tags because this is a DURABLE format: a field rename must be a deliberate
// migration, not a side effect of renaming a Go field.
//
// DecidedAt round-trips through RFC 3339 with nanoseconds (encoding/json's time.Time format), which
// preserves the instant exactly — and it has to, because DecidedAt is the per-hull COOLDOWN CLOCK.
// A DecidedAt that drifted or reset on load would either re-open a hull for relocation early or
// freeze it forever, and it is the one field a restart cannot re-derive from live state.
type relocationIntentRecord struct {
	ShipSymbol     string    `json:"ship_symbol"`
	FromSystem     string    `json:"from_system"`
	TargetSystem   string    `json:"target_system"`
	TargetWaypoint string    `json:"target_waypoint"`
	DecidedAt      time.Time `json:"decided_at"`
	Completed      bool      `json:"completed"`
}

// relocationIntentContainerStore is the narrow container-config read/write the intent store needs.
// Satisfied by *persistence.ContainerRepositoryGORM.
type relocationIntentContainerStore interface {
	Get(ctx context.Context, containerID string, playerID int) (*persistence.ContainerModel, error)
	UpdateContainerConfig(ctx context.Context, containerID string, playerID int, configJSON string) error
}

// RelocationIntentConfigStore backs the relocator's RelocationIntentStore with the relocator
// container's OWN config column, mirroring TourRepositionConfigPersister (sp-zhii): a read-modify-write
// of the config map guarded to a SINGLE key, so it never clobbers the launch knobs the recovery
// rebuild reads or the status/heartbeat columns the runner updates concurrently.
//
// Why the container config and not a table: the intents are the reconciler's own restart state, they
// are one small record per hull, and the container config is already the durable home for exactly this
// (the tour's in-flight reposition target, the arb's buy costs). A dedicated table would add a
// migration and a second recovery path for state that dies with the container anyway.
//
// One record per hull, REWRITTEN IN PLACE, serving two restart duties at once (see
// tradingCmd.RelocationIntent): while Completed is false it is an in-flight move a restart must
// FINISH rather than re-decide; once Completed it is the hull's cooldown clock.
type RelocationIntentConfigStore struct {
	containers relocationIntentContainerStore
}

// NewRelocationIntentConfigStore wires the config-backed relocation-intent store.
func NewRelocationIntentConfigStore(containers relocationIntentContainerStore) *RelocationIntentConfigStore {
	return &RelocationIntentConfigStore{containers: containers}
}

// LoadRelocationIntents returns every persisted intent for the container, completed and not.
//
// An ERROR here is load-bearing and must never be softened to an empty list: the relocator refuses to
// proceed on unreadable intents precisely because, without knowing what is in flight, it could
// re-decide a move already authorised (the double-relocation bug) and would read every cooldown as
// "never relocated". An ABSENT key, by contrast, is not an error — it is a relocator that has decided
// nothing yet.
func (s *RelocationIntentConfigStore) LoadRelocationIntents(ctx context.Context, containerID string, playerID int) ([]tradingCmd.RelocationIntent, error) {
	config, err := s.readConfig(ctx, containerID, playerID)
	if err != nil {
		return nil, err
	}
	raw, present := config[relocationIntentsConfigKey]
	if !present || raw == nil {
		return nil, nil // nothing decided yet — not a failure
	}
	// The config arrives as map[string]interface{}, so the stored object is re-marshalled and decoded
	// into the typed record. Failing CLOSED on a corrupt payload is deliberate: silently treating
	// unparseable intents as "none" is exactly how an in-flight move gets re-decided.
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("re-encode persisted relocation intents of container %s: %w", containerID, err)
	}
	var records map[string]relocationIntentRecord
	if err := json.Unmarshal(encoded, &records); err != nil {
		return nil, fmt.Errorf("deserialize persisted relocation intents of container %s: %w", containerID, err)
	}
	intents := make([]tradingCmd.RelocationIntent, 0, len(records))
	for ship, record := range records {
		symbol := record.ShipSymbol
		if symbol == "" {
			symbol = ship // tolerate a record whose key is the only surviving identity
		}
		intents = append(intents, tradingCmd.RelocationIntent{
			ShipSymbol:     symbol,
			FromSystem:     record.FromSystem,
			TargetSystem:   record.TargetSystem,
			TargetWaypoint: record.TargetWaypoint,
			DecidedAt:      record.DecidedAt,
			Completed:      record.Completed,
		})
	}
	return intents, nil
}

// RecordRelocationIntent writes (or rewrites) the single record for intent.ShipSymbol, preserving
// every other key in the config — including the other hulls' intents and the container's launch
// knobs.
//
// The relocator calls this BEFORE it moves a hull and again once the move lands, and it refuses to
// move at all when this returns an error (a hull is never sent anywhere the store could not record).
// So an error here is a stop, not a warning, and every failure mode is surfaced verbatim rather than
// swallowed.
func (s *RelocationIntentConfigStore) RecordRelocationIntent(ctx context.Context, containerID string, playerID int, intent tradingCmd.RelocationIntent) error {
	if intent.ShipSymbol == "" {
		return fmt.Errorf("cannot record a relocation intent with no ship symbol on container %s", containerID)
	}
	config, err := s.readConfig(ctx, containerID, playerID)
	if err != nil {
		return err
	}
	records := map[string]relocationIntentRecord{}
	if raw, present := config[relocationIntentsConfigKey]; present && raw != nil {
		encoded, merr := json.Marshal(raw)
		if merr != nil {
			return fmt.Errorf("re-encode existing relocation intents of container %s: %w", containerID, merr)
		}
		if uerr := json.Unmarshal(encoded, &records); uerr != nil {
			return fmt.Errorf("deserialize existing relocation intents of container %s: %w", containerID, uerr)
		}
	}
	records[intent.ShipSymbol] = relocationIntentRecord{
		ShipSymbol:     intent.ShipSymbol,
		FromSystem:     intent.FromSystem,
		TargetSystem:   intent.TargetSystem,
		TargetWaypoint: intent.TargetWaypoint,
		DecidedAt:      intent.DecidedAt,
		Completed:      intent.Completed,
	}
	config[relocationIntentsConfigKey] = records

	merged, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("serialize container %s config after merging relocation intents: %w", containerID, err)
	}
	return s.containers.UpdateContainerConfig(ctx, containerID, playerID, string(merged))
}

// readConfig loads the container's config as a mutable map. A missing container row (already
// terminalized) is an error the caller surfaces: unlike the tour's reposition persister — where
// persistence is advisory durability — this store gates MOVEMENT, so a lost row must stop the
// relocation rather than let it proceed unrecorded.
func (s *RelocationIntentConfigStore) readConfig(ctx context.Context, containerID string, playerID int) (map[string]interface{}, error) {
	if s == nil || s.containers == nil {
		return nil, fmt.Errorf("opportunity relocator intent store is not wired to a container repository")
	}
	model, err := s.containers.Get(ctx, containerID, playerID)
	if err != nil {
		return nil, fmt.Errorf("load container %s to read relocation intents: %w", containerID, err)
	}
	if model == nil {
		return nil, fmt.Errorf("container %s not found - cannot read or record relocation intents", containerID)
	}
	config := map[string]interface{}{}
	if model.Config != "" {
		if err := json.Unmarshal([]byte(model.Config), &config); err != nil {
			return nil, fmt.Errorf("deserialize container %s config: %w", containerID, err)
		}
	}
	return config, nil
}
