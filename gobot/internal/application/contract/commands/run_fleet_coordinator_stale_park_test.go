package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// The measured era torwind-2026-08-09 geometry, laid on one axis so the live
// distances are reproduced exactly: contract cmsm83jvx sourced COPPER at H57
// for delivery at F55, and the coordinator flew TORWIND-7 766.83 units while
// TORWIND-5 sat DOCKED on H57.
const (
	staleParkSource      = "X1-DU34-H57" // the sourcing plan's market — TORWIND-5 is docked here
	staleParkDestination = "X1-DU34-F55" // the delivery
	staleParkFarBerth    = "X1-DU34-B12" // where TORWIND-7 idled, 766.83 from the source
)

// staleParkShipRepo separates the PERSISTED fleet snapshot the parking decision
// reads (FindAllByPlayer/FindBySymbol) from SERVER TRUTH (SyncShipFromAPI), which
// is the whole shape of this TOCTOU: the persisted row still shows the hold the
// hull emptied seconds ago. SyncShipFromAPI writes the server answer back over the
// persisted row exactly as the real repository does.
type staleParkShipRepo struct {
	navigation.ShipRepository

	order     []string
	persisted map[string]*navigation.Ship
	server    map[string]*navigation.Ship

	claimErr map[string]error    // injected ClaimShip rejection, by ship symbol
	claims   []contractShipClaim // claims the DB accepted
	rejected []contractShipClaim // claims the DB refused
	syncs    []string
}

func (r *staleParkShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	ships := make([]*navigation.Ship, 0, len(r.order))
	for _, symbol := range r.order {
		ships = append(ships, r.persisted[symbol])
	}
	return ships, nil
}

func (r *staleParkShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	ship, ok := r.persisted[symbol]
	if !ok {
		return nil, errors.New("ship not found: " + symbol)
	}
	return ship, nil
}

func (r *staleParkShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	var matched []*navigation.Ship
	for _, symbol := range r.order {
		if ship := r.persisted[symbol]; ship.ContainerID() == containerID {
			matched = append(matched, ship)
		}
	}
	return matched, nil
}

func (r *staleParkShipRepo) SyncShipFromAPI(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.syncs = append(r.syncs, symbol)
	live, ok := r.server[symbol]
	if !ok {
		return nil, errors.New("ship not found on server: " + symbol)
	}
	r.persisted[symbol] = live // the sync persists server truth, as production does
	return live, nil
}

func (r *staleParkShipRepo) ClaimShip(_ context.Context, symbol, containerID string, _ shared.PlayerID, operation string) error {
	claim := contractShipClaim{symbol: symbol, containerID: containerID, operation: operation}
	if err, rejected := r.claimErr[symbol]; rejected {
		r.rejected = append(r.rejected, claim)
		return err
	}
	r.claims = append(r.claims, claim)
	return nil
}

func (r *staleParkShipRepo) Save(_ context.Context, _ *navigation.Ship) error { return nil }

func (r *staleParkShipRepo) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	sh, err := r.FindBySymbol(ctx, symbol, playerID)
	if err != nil {
		return nil, false, err
	}
	changed, err := mutate(sh)
	if err != nil {
		return sh, false, err
	}
	return sh, changed, nil
}

// contractWorkClaims narrows the recorded claims to CONTRACT_WORKFLOW dispatches,
// so a cargo_liquidation claim on the same hull is never mistaken for one.
func (r *staleParkShipRepo) contractWorkClaims() []contractShipClaim {
	var out []contractShipClaim
	for _, c := range r.claims {
		if strings.HasPrefix(c.containerID, "contract-work") {
			out = append(out, c)
		}
	}
	return out
}

// staleParkHull builds an idle contract-DEDICATED hauler at a waypoint, holding
// `units` of `good` (units 0 = an empty hold).
func staleParkHull(t *testing.T, symbol, waypointSymbol string, x float64, good string, units int) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint(waypointSymbol, x, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(600, 600)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	var inventory []*shared.CargoItem
	if units > 0 {
		item, itemErr := shared.NewCargoItem(good, good, "", units)
		if itemErr != nil {
			t.Fatalf("NewCargoItem: %v", itemErr)
		}
		inventory = append(inventory, item)
	}
	cargo, err := shared.NewCargo(80, units, inventory)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), location, fuel, 600, 80, cargo, 9,
		"FRAME_HAULER", "HAULER", nil, navigation.NavStatusDocked)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	ship.SetDedicatedFleet(dedicatedFleetContract)
	return ship
}

func staleParkGraph(t *testing.T) *system.NavigationGraph {
	t.Helper()
	waypoints := map[string]*shared.Waypoint{}
	for symbol, x := range map[string]float64{
		staleParkSource:      0,
		staleParkFarBerth:    766.83,
		staleParkDestination: 400,
	} {
		wp, err := shared.NewWaypoint(symbol, x, 0)
		if err != nil {
			t.Fatalf("NewWaypoint(%s): %v", symbol, err)
		}
		waypoints[symbol] = wp
	}
	return &system.NavigationGraph{Waypoints: waypoints}
}

func staleParkContract(t *testing.T) *domainContract.Contract {
	t.Helper()
	terms := domainContract.Terms{
		Payment: domainContract.Payment{OnAccepted: 8643, OnFulfilled: 25931},
		Deliveries: []domainContract.Delivery{
			{TradeSymbol: "COPPER", DestinationSymbol: staleParkDestination, UnitsRequired: 59, UnitsFulfilled: 0},
		},
		DeadlineToAccept: "2026-01-01T00:00:00Z",
		Deadline:         "2027-01-01T00:00:00Z",
	}
	c, err := domainContract.NewContract("cmsm83jvx", shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", terms, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	if err := c.Accept(); err != nil {
		t.Fatalf("seed Accept: %v", err)
	}
	return c
}

// runStaleParkPass drives the real coordinator loop over the given fleet until it
// dispatches and parks on its worker (or the deadline elapses), and reports what
// the fleet's ONE contract dispatch was.
func runStaleParkPass(t *testing.T, repo *staleParkShipRepo) (*staleParkShipRepo, *spawnContractFakeDaemonClient) {
	t.Helper()
	daemonClient := &spawnContractFakeDaemonClient{}
	handler := &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, &reclaimFakeContainerRepo{}, repo),
		contractMarketService:  contractServices.NewContractMarketService(nil, &activeContractRepo{c: staleParkContract(t)}),
		shipRepo:               repo,
		daemonClient:           daemonClient,
		graphProvider:          &placementStubGraphProvider{graph: staleParkGraph(t)},
		clock:                  &shared.MockClock{CurrentTime: time.Now()},
		eventSubscriber:        &reclaimFakeSubscriber{workerCompleted: make(chan navigation.WorkerCompletedEvent)},
	}
	// Inventory-first sourcing puts the plan's market on the SOURCE waypoint with
	// no market fake, exactly where the live plan bought (H57).
	handler.SetInventoryFinder(inventoryFinderStub{good: "COPPER", units: 59, waypoint: staleParkSource})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _ = handler.Handle(ctx, contractSpawnCommand())
	return repo, daemonClient
}

// staleParkFleet is the measured live fleet: TORWIND-5 docked ON the source market
// whose PERSISTED row still shows the FABRICS lot it has already shed, and TORWIND-7
// idle and empty 766.83 units away.
func staleParkFleet(t *testing.T, serverHoldCleared bool) *staleParkShipRepo {
	t.Helper()
	parkedPersisted := staleParkHull(t, "TORWIND-5", staleParkSource, 0, "FABRICS", 30)
	parkedServer := staleParkHull(t, "TORWIND-5", staleParkSource, 0, "FABRICS", 30)
	if serverHoldCleared {
		parkedServer = staleParkHull(t, "TORWIND-5", staleParkSource, 0, "FABRICS", 0)
	}
	far := staleParkHull(t, "TORWIND-7", staleParkFarBerth, 766.83, "COPPER", 0)

	return &staleParkShipRepo{
		order:     []string{"TORWIND-5", "TORWIND-7"},
		persisted: map[string]*navigation.Ship{"TORWIND-5": parkedPersisted, "TORWIND-7": far},
		server:    map[string]*navigation.Ship{"TORWIND-5": parkedServer, "TORWIND-7": far},
		claimErr:  map[string]error{},
	}
}

// LIVE BUG sp-ban71 (era torwind-2026-08-09, contract cmsm83jvx, COPPER x59 ->
// X1-DU34-F55). TORWIND-5 was parked out of the candidate pool for "holding
// unrelated cargo" on a cargo read taken at the PARKING decision. Before dispatch
// its hold emptied; the coordinator DETECTED that itself ("its hold cleared between
// the parking decision and dispatch") and correctly declined to spawn a pointless
// liquidation — but detecting the void exclusion did not re-admit the hull, so
// selection ran over the remaining candidates and flew TORWIND-7 766.83 units while
// TORWIND-5 sat DOCKED on the very market the sourcing plan buys from. The pool must
// reflect the hold as it stands at DISPATCH, not at parking.
func TestFleetCoordinator_ParkedHullWhoseHoldClearedBeforeDispatch_WinsSelection(t *testing.T) {
	repo, daemonClient := runStaleParkPass(t, staleParkFleet(t, true))

	dispatches := repo.contractWorkClaims()
	if len(dispatches) != 1 {
		t.Fatalf("expected exactly one contract dispatch this pass, got %d: %+v", len(dispatches), dispatches)
	}
	if dispatches[0].symbol != "TORWIND-5" {
		t.Fatalf("the hull whose hold cleared before dispatch sits ON the source market (distance 0) and MUST win selection; the coordinator dispatched %q instead (the 766.83-unit ferry)", dispatches[0].symbol)
	}
	if dispatches[0].operation != dedicatedFleetContract {
		t.Fatalf("re-admission must claim under the SAME contract fleet identity as any other dispatch (RULINGS #7), got operation %q", dispatches[0].operation)
	}
	if len(daemonClient.started) != 1 || !strings.Contains(daemonClient.started[0], "TORWIND-5") {
		t.Fatalf("expected exactly one worker started, on TORWIND-5, got %v", daemonClient.started)
	}
}

// THE GUARD MUST STILL BIND. Same fleet, but the server CONFIRMS TORWIND-5 is still
// holding the FABRICS lot. The NO-CARGO-DUMP guard exists so a worker is never
// dispatched onto foreign cargo it would jettison — so the confirmed-laden hull stays
// out of the pool however close it is, and the far EMPTY hull takes the contract.
func TestFleetCoordinator_ParkedHullStillHoldingUnrelatedCargo_StaysExcluded(t *testing.T) {
	repo, daemonClient := runStaleParkPass(t, staleParkFleet(t, false))

	dispatches := repo.contractWorkClaims()
	if len(dispatches) != 1 {
		t.Fatalf("expected exactly one contract dispatch this pass, got %d: %+v", len(dispatches), dispatches)
	}
	if dispatches[0].symbol != "TORWIND-7" {
		t.Fatalf("a hull the server CONFIRMS is still holding unrelated cargo must never be dispatched onto a contract (its foreign lot would be jettisoned); got %q", dispatches[0].symbol)
	}
	for _, started := range daemonClient.started {
		if strings.HasPrefix(started, "contract-work") && strings.Contains(started, "TORWIND-5") {
			t.Fatalf("a CONTRACT_WORKFLOW worker was started on the confirmed-laden hull: %v", daemonClient.started)
		}
	}
}

// RULINGS #7: re-admission is not a back door around the claim. A hull the DB refuses
// to claim — pinned to another fleet, or already taken by a concurrent operation — is
// never dispatched by the re-admission path, however close it sits to the source.
func TestFleetCoordinator_ReadmittedHullRejectedByClaim_IsNeverDispatched(t *testing.T) {
	fleet := staleParkFleet(t, true)
	fleet.claimErr["TORWIND-5"] = errors.New("ship TORWIND-5 is assigned to container arb-leg-9 (operation mismatch)")

	repo, daemonClient := runStaleParkPass(t, fleet)

	if len(repo.rejected) == 0 {
		t.Fatalf("anti-vacuity: the re-admitted hull never even reached ClaimShip — the claim guard was not exercised")
	}
	for _, claim := range repo.claims {
		if claim.symbol == "TORWIND-5" {
			t.Fatalf("a hull the DB refused must never be recorded as claimed: %+v", claim)
		}
	}
	for _, started := range daemonClient.started {
		if strings.Contains(started, "TORWIND-5") {
			t.Fatalf("a worker was started on a hull whose claim the DB refused: %v", daemonClient.started)
		}
	}
}
