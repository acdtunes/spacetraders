package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// --- the standing yard-sentinel probe ---
//
// A 4th probe, bought once per era beyond the probeTarget scouting seed, captain-reserved (never
// dedicated-fleet tagged) and parked docked at the home shipyard for the rest of COLDSTART/GATE so
// every PriceCheck reads warm without diverting an earning hull. These pin bootstrap's OWN half of the
// feature: the sequencing/capital gate on the buy, the idempotent positioning, and the EXPANSION
// release. The other half — that a released sentinel is actually ADOPTED by parked-sensing — is pinned
// in internal/application/scouting/commands (adoptStrandedProbes is unexported there and this package
// must not import that adapter-facing layer), and the fact that selectHomeTourHulls never selects a
// captain-reserved sentinel is pinned in internal/adapters/grpc (where that function lives). Three
// packages, three tests, one seam each — the only shape the import graph allows (bootstrap/commands and
// scouting/commands are siblings; grpc depends on both and would cycle if either depended back).

// fakeYardSentinelAcquirer is the sentinel's whole lifecycle, spied.
type fakeYardSentinelAcquirer struct {
	price     int64
	yard      string
	readable  bool
	priceErr  error
	priceChks int

	buyErr        error
	buys          int
	boughtSymbol  string
	lastReason    string
	lastPurchaser string

	parkCalls int
	parkErr   error
	docked    bool
	parkTypes []string // the ship type each EnsureParked call was asked to stand watch for

	releaseCalls   int
	releaseErr     error
	releasedSymbol string
	releasedReason string
}

func (f *fakeYardSentinelAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	f.priceChks++
	if f.priceErr != nil || !f.readable {
		return 0, "", false, f.priceErr
	}
	return f.price, f.yard, true, nil
}

func (f *fakeYardSentinelAcquirer) BuyAndReserve(ctx context.Context, playerID int, shipType, yard, reason, purchaserSymbol string) (BuyResult, error) {
	if f.buyErr != nil {
		return BuyResult{}, f.buyErr
	}
	f.buys++
	f.lastReason = reason
	f.lastPurchaser = purchaserSymbol
	sym := f.boughtSymbol
	if sym == "" {
		sym = "SENTINEL-NEW"
	}
	return BuyResult{ShipSymbol: sym, Price: f.price}, nil
}

func (f *fakeYardSentinelAcquirer) EnsureParked(ctx context.Context, playerID int, homeSystem, shipType, shipSymbol string) (bool, error) {
	f.parkCalls++
	f.parkTypes = append(f.parkTypes, shipType)
	if f.parkErr != nil {
		return false, f.parkErr
	}
	return f.docked, nil
}

func (f *fakeYardSentinelAcquirer) Release(ctx context.Context, playerID int, shipSymbol, reason string) error {
	f.releaseCalls++
	if f.releaseErr != nil {
		return f.releaseErr
	}
	f.releasedSymbol = shipSymbol
	f.releasedReason = reason
	return nil
}

// --- the buy: sequencing behind the scouting seed ---

// The sentinel must NOT compete with the higher-priority 3-probe scouting seed for the same tick's
// capital: below target, the sentinel buy does nothing at all.
func TestBootstrap_YardSentinel_DoesNotBuyBeforeScoutsAreAtTarget(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget - 1
	obs.Treasury = 1_000_000
	ys := &fakeYardSentinelAcquirer{price: 40_000, yard: "X1-HQ-YARD", readable: true}
	h, spies := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ys.buys != 0 || ys.priceChks != 0 {
		t.Fatalf("the sentinel must wait for the scouting seed to reach target first, got buys=%d price_checks=%d", ys.buys, ys.priceChks)
	}
	if spies.probes.buys == 0 {
		t.Fatalf("control: the scouting ramp itself must still be buying")
	}
}

// Once the scouting seed IS at target, the sentinel buy fires.
func TestBootstrap_YardSentinel_BuysOnceScoutsAreAtTarget(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	obs.Treasury = 1_000_000
	ys := &fakeYardSentinelAcquirer{price: 40_000, yard: "X1-HQ-YARD", readable: true}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ys.buys != 1 {
		t.Fatalf("expected exactly one sentinel buy, got %d", ys.buys)
	}
	if ys.lastReason != YardSentinelReservationReason {
		t.Fatalf("the bought hull must be reserved with the sentinel's OWN reason, got %q want %q", ys.lastReason, YardSentinelReservationReason)
	}
	if !res.YardSentinelBought {
		t.Fatalf("expected res.YardSentinelBought=true")
	}
}

// The buy must reuse the SAME probe ship type every scouting probe uses — this is meant to read as an
// extra probe, not a different asset.
func TestBootstrap_YardSentinel_BuysTheProbeShipType(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	obs.Treasury = 1_000_000
	ys := &fakeYardSentinelAcquirer{price: 40_000, yard: "X1-HQ-YARD", readable: true}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ys.buys != 1 {
		t.Fatalf("expected one buy, got %d", ys.buys)
	}
}

// --- the buy: capital gate reuses common.ImmutableReserveFloor, never a second threshold ---

// Clears the SAME flat floor the scouting probe buy uses (RULINGS #5) — never the stricter
// contractWorkingCapitalFloor haulers/gate-workers reserve against.
func TestBootstrap_YardSentinel_CapitalGate_ClearsOnImmutableReserveFloorAlone(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	price := int64(40_000)
	// Clears common.ImmutableReserveFloor (50k) but falls well short of contractWorkingCapitalFloor
	// (350k) — proving the sentinel is gated on the LOWER, probe-buy floor, not the hauler-class one.
	obs.Treasury = price + common.ImmutableReserveFloor + 1_000
	if obs.Treasury-price >= contractWorkingCapitalFloor {
		t.Fatalf("test fixture invalid: treasury also clears the working-capital floor, so this test would not discriminate between the two")
	}
	ys := &fakeYardSentinelAcquirer{price: price, yard: "X1-HQ-YARD", readable: true}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ys.buys != 1 {
		t.Fatalf("the sentinel buy must clear on the immutable floor alone, got buys=%d", ys.buys)
	}
}

// A cushion below the immutable floor blocks the buy — fails closed, exactly like the scouting buy.
func TestBootstrap_YardSentinel_CapitalGate_BlocksBelowImmutableFloor(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	price := int64(40_000)
	obs.Treasury = price + common.ImmutableReserveFloor - 1 // one credit short
	ys := &fakeYardSentinelAcquirer{price: price, yard: "X1-HQ-YARD", readable: true}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	log := &capturingLogger{}
	if _, err := h.reconcileOnce(ctxWithLogger(log), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ys.buys != 0 {
		t.Fatalf("must not buy below the immutable reserve floor, got buys=%d", ys.buys)
	}
	if !log.has("bootstrap_yard_sentinel_buy_decision") {
		t.Fatalf("the decision must still be logged, affordable or not (captain L61)")
	}
}

// --- readiness / wiring: never claims res.Blocker, the money guards' single-valued field ---

// No idle hull to execute the buy ⇒ blocked, not failed, and — like the tour start — never claims
// res.Blocker, which belongs to the higher-priority money guards.
func TestBootstrap_YardSentinel_NoIdlePurchaser_BlocksWithoutClaimingResBlocker(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	obs.HasIdlePurchaser = false
	ys := &fakeYardSentinelAcquirer{price: 40_000, yard: "X1-HQ-YARD", readable: true}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ys.buys != 0 || ys.priceChks != 0 {
		t.Fatalf("no idle hull ⇒ no price check, no buy, got price_checks=%d buys=%d", ys.priceChks, ys.buys)
	}
	if res.Blocker == "no_purchaser" {
		t.Fatalf("the sentinel must never claim res.Blocker — it is a nice-to-have that must not mask why a scouting/contract buy could not happen")
	}
}

// An unwired acquirer says so LOUDLY (never a panic, never silent) but still never claims res.Blocker.
//
// Driven DIRECTLY against actYardSentinel rather than through reconcileOnce: res.Blocker is
// single-valued for the WHOLE tick, and freshDataObs's other cold-start workstreams (e.g. the contract
// placement gate) legitimately claim it for their own unrelated reasons in a full tick — asserting
// res.Blocker=="" at that level would fail for reasons that have nothing to do with the sentinel. This
// isolates exactly the property under test: the sentinel's OWN step never sets it, whatever else is
// wired.
func TestBootstrap_YardSentinel_NoAcquirerWired_IsLoudNotPanic(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	h := NewRunBootstrapCoordinatorHandler(nil) // deliberately no SetYardSentinelAcquirer

	log := &capturingLogger{}
	res := reconcileResult{}
	h.actYardSentinel(ctxWithLogger(log), baseCmd(), defaultTargets(), obs, &res)
	if res.Blocker != "" {
		t.Fatalf("an unwired acquirer must not claim res.Blocker, got %q", res.Blocker)
	}
	if !log.has("bootstrap_yard_sentinel_no_acquirer") {
		t.Fatalf("an unwired acquirer must surface loudly on its own line")
	}
}

// A cold (unreadable) yard price positions a hull rather than failing silently — reuses the SAME
// awaitReadablePrice dance every other bootstrap buy uses.
func TestBootstrap_YardSentinel_ColdPrice_PositionsRatherThanFailsSilently(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	ys := &fakeYardSentinelAcquirer{readable: false}
	scanner := &fakeScanner{dispatched: true}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)
	h.SetShipyardScanner(scanner)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ys.buys != 0 {
		t.Fatalf("a cold price must never buy, got %d", ys.buys)
	}
	if scanner.calls == 0 {
		t.Fatalf("a cold price must position a hull at the yard so the next tick's read succeeds")
	}
}

// --- one-shot: never buys a second sentinel ---

func TestBootstrap_YardSentinel_NeverBuysASecondOnceOneExists(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	obs.YardSentinelSymbol = "SENTINEL-1"
	obs.YardSentinelParked = true
	ys := &fakeYardSentinelAcquirer{price: 40_000, yard: "X1-HQ-YARD", readable: true, docked: true}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	for tick := 1; tick <= 10; tick++ {
		if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
	}
	if ys.buys != 0 {
		t.Fatalf("a sentinel that already exists must never be bought again, got %d buys over 10 ticks", ys.buys)
	}
}

// --- positioning: re-evaluated every tick so a shifted need can redirect it (sp-ltl75) ---
//
// Before the fix, EnsureParked stopped being called the instant obs.YardSentinelParked went true — a
// purely POSITIONAL fact (docked at SOME shipyard) the reconciler treated as a terminal latch. That froze
// the sentinel wherever it first parked even once bootstrap's ramp moved on to a ship type a DIFFERENT
// yard sells (confirmed live: the sentinel parked at the probe yard, bootstrap moved on to buying
// haulers, and the sentinel never reconsidered). The fix re-derives the need (sentinelShipTypeNeed) and
// keeps calling EnsureParked every tick for as long as something is still being bought, so a real
// yard-selection implementation gets the chance to redirect a hull sitting somewhere the current need has
// outgrown — pinned for real at the grpc adapter in TestEnsureParked_RepositionsWhenDockedAtAYardConfirmedWrongForTheCurrentType.

func TestBootstrap_YardSentinel_KeepsReVerifyingPlacementWhileAnyRampStillNeedsToBuy(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	obs.YardSentinelSymbol = "SENTINEL-1"
	obs.YardSentinelParked = false
	ys := &fakeYardSentinelAcquirer{docked: false}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if ys.parkCalls != 1 {
		t.Fatalf("expected one EnsureParked call while not yet parked, got %d", ys.parkCalls)
	}
	if len(ys.parkTypes) != 1 || ys.parkTypes[0] != haulerShipType {
		t.Fatalf("probes are already at target and the contract-hauler ramp is still short — the sentinel must stand watch for %s, got %v", haulerShipType, ys.parkTypes)
	}
	if ys.buys != 0 {
		t.Fatalf("must not re-buy once the sentinel already exists, got buys=%d", ys.buys)
	}
	if res.YardSentinelDocked {
		t.Fatalf("must not report docked while EnsureParked still returns false")
	}

	// The next tick's fresh observation reports it docked (the adapter's own idempotent state, or an
	// observer re-read). The hauler ramp is STILL short of target, so EnsureParked must be RE-VERIFIED,
	// never frozen by the mere fact of being docked — that freeze was the bug.
	ys.docked = true
	docked := obs
	docked.YardSentinelParked = true
	h.SetWorldObserver(&fakeObserver{obs: docked})
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if ys.parkCalls != 2 {
		t.Fatalf("still short of the hauler ramp target — EnsureParked must be re-verified every tick, not frozen once docked, got %d calls", ys.parkCalls)
	}

	// Once the hauler ramp ALSO reaches target there is genuinely nothing left to stand watch for, and
	// the sentinel correctly HOLDS its position rather than being redirected on no evidence.
	fullyStaffed := docked
	fullyStaffed.Haulers = make([]HaulerSnapshot, defaultTargets().HaulerTarget)
	h.SetWorldObserver(&fakeObserver{obs: fullyStaffed})
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if ys.parkCalls != 2 {
		t.Fatalf("every ramp is at target — EnsureParked must not be called with no evidence of any need, got %d calls", ys.parkCalls)
	}
}

// A positioning failure is retried, logged, and never claims res.Blocker. Driven directly against
// actYardSentinel for the same isolation reason as the unwired-acquirer test above.
func TestBootstrap_YardSentinel_PositionError_RetriedNeverBlocks(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget
	obs.YardSentinelSymbol = "SENTINEL-1"
	ys := &fakeYardSentinelAcquirer{parkErr: errors.New("routing service down")}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetYardSentinelAcquirer(ys)

	log := &capturingLogger{}
	res := reconcileResult{}
	h.actYardSentinel(ctxWithLogger(log), baseCmd(), defaultTargets(), obs, &res)
	if res.Blocker != "" {
		t.Fatalf("positioning must never claim res.Blocker, got %q", res.Blocker)
	}
	if !log.has("bootstrap_yard_sentinel_position_error") {
		t.Fatalf("the failure must surface on its own line")
	}

	res2 := reconcileResult{}
	h.actYardSentinel(ctxWithLogger(&capturingLogger{}), baseCmd(), defaultTargets(), obs, &res2)
	if ys.parkCalls != 2 {
		t.Fatalf("a failed positioning attempt must be retried next tick, got %d calls", ys.parkCalls)
	}
}

// --- EXPANSION: the release ---

func TestBootstrap_Expansion_ReleasesTheYardSentinel(t *testing.T) {
	obs := matureObs()
	obs.YardSentinelSymbol = "SENTINEL-1"
	ys := &fakeYardSentinelAcquirer{}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ys.releaseCalls != 1 || ys.releasedSymbol != "SENTINEL-1" {
		t.Fatalf("expected exactly one release of SENTINEL-1, got calls=%d symbol=%q", ys.releaseCalls, ys.releasedSymbol)
	}
	if !res.YardSentinelReleased {
		t.Fatalf("expected res.YardSentinelReleased=true")
	}
	if !res.Done {
		t.Fatalf("the release must not hold the terminal exit, got Done=%v", res.Done)
	}
	if !log.has("bootstrap_yard_sentinel_released") {
		t.Fatalf("the release must surface on its own log line (observability)")
	}
}

// No sentinel ever existed this era ⇒ zero calls (the ordinary case for a fleet that started before
// this feature shipped, or on which the sentinel buy never cleared capital).
func TestBootstrap_Expansion_NoSentinelBought_ReleasesNothing(t *testing.T) {
	ys := &fakeYardSentinelAcquirer{}
	h, _ := spiedHandler(matureObs(), &fakeHandoff{}) // matureObs carries no YardSentinelSymbol
	h.SetYardSentinelAcquirer(ys)

	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if ys.releaseCalls != 0 {
		t.Fatalf("no sentinel ever bought this era — release must make NO call, got %d", ys.releaseCalls)
	}
}

// A release failure must NEVER hold the terminal exit and must claim no blocker — a mature fleet is
// never pinned in the per-tick full-fleet re-read over one hull's reservation. Retried next tick.
func TestBootstrap_Expansion_YardSentinelReleaseError_NeverBlocksExit_AndRetries(t *testing.T) {
	obs := matureObs()
	obs.YardSentinelSymbol = "SENTINEL-1"
	ys := &fakeYardSentinelAcquirer{releaseErr: errors.New("db down")}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	log1 := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log1), baseCmd())
	if err != nil {
		t.Fatalf("tick 1: reconcileOnce: %v", err)
	}
	if !res.Done {
		t.Fatalf("a failed release must never hold the terminal exit, got Done=%v", res.Done)
	}
	if res.Blocker != "" {
		t.Fatalf("the release is best-effort and must claim no blocker, got %q", res.Blocker)
	}
	if ys.releaseCalls != 1 || !log1.has("bootstrap_yard_sentinel_release_error") {
		t.Fatalf("the failed release must have been attempted and surfaced, got calls=%d", ys.releaseCalls)
	}

	// Retried on the next EXPANSION tick (a restart, or the same container's bounded-retry window).
	ys.releaseErr = nil
	log2 := &capturingLogger{}
	if _, err := h.reconcileOnce(ctxWithLogger(log2), baseCmd()); err != nil {
		t.Fatalf("tick 2: reconcileOnce: %v", err)
	}
	if ys.releaseCalls != 2 || !log2.has("bootstrap_yard_sentinel_released") {
		t.Fatalf("the release must be retried after a failure, got calls=%d", ys.releaseCalls)
	}
}

// An unwired collaborator at EXPANSION is a logged skip, never a panic, never a held exit.
func TestBootstrap_Expansion_NoYardSentinelCollaboratorWired_SkipsWithoutBlockingExit(t *testing.T) {
	obs := matureObs()
	obs.YardSentinelSymbol = "SENTINEL-1"
	h, _ := spiedHandler(obs, &fakeHandoff{}) // deliberately no SetYardSentinelAcquirer

	log := &capturingLogger{}
	res, err := h.reconcileOnce(ctxWithLogger(log), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if !res.Done {
		t.Fatalf("an unwired collaborator must not hold the terminal exit, got Done=%v", res.Done)
	}
	if !log.has("bootstrap_yard_sentinel_release_skipped") {
		t.Fatalf("an unwired collaborator must surface the skip on its own line")
	}
}

// DRY-RUN releases nothing.
func TestBootstrap_Expansion_DryRun_ReleasesNoSentinel(t *testing.T) {
	obs := matureObs()
	obs.YardSentinelSymbol = "SENTINEL-1"
	ys := &fakeYardSentinelAcquirer{}
	h, _ := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	log := &capturingLogger{}
	res := reconcileResult{}
	h.actExpansion(ctxWithLogger(log), baseCmd(), bootstrapRunConfig{DryRun: true}, obs, &res)
	if ys.releaseCalls != 0 {
		t.Fatalf("dry-run must release nothing, got %d calls", ys.releaseCalls)
	}
	if !log.has("bootstrap_would_release_yard_sentinel") {
		t.Fatalf("dry-run must log the WOULD line for the release")
	}
}

// --- THE MANDATORY HANDOFF TEST: bootstrap's own half, across the REAL phase transition ---
//
// This drives the reconciler through a scripted COLDSTART → GATE → EXPANSION arc — not an isolated
// call to actYardSentinel or actExpansion — and proves bootstrap's own end of the hand-off: the
// sentinel is bought once the scouting seed is complete, parked, survives the whole GATE phase
// untouched, and is released the moment EXPANSION is reached. The other half of the proof — that the
// released hull is actually ADOPTED by parked-sensing, not left a silent permanent orphan — is in
// internal/application/scouting/commands/run_probe_sensing_yard_sentinel_handoff_test.go
// (TestYardSentinelHandoff_ReleasedSentinelIsAdoptedIntoParkedSensing), which starts from exactly the
// released state this test ends in. adoptStrandedProbes is unexported in that sibling package and this
// one must not import the grpc adapter layer that could reach it without cycling back here, so the
// full loop is necessarily two tests joined at that seam — the only shape the import graph allows.
func TestBootstrap_YardSentinel_ColdStartThroughExpansion_BootstrapReleasesForAdoption(t *testing.T) {
	obs := freshDataObs()
	obs.Treasury = 5_000_000
	ys := &fakeYardSentinelAcquirer{price: 40_000, yard: "X1-HQ-YARD", readable: true}
	h, spies := spiedHandler(obs, &fakeHandoff{})
	h.SetYardSentinelAcquirer(ys)

	// Tick 1: cold agent, 0 probes. The scouting seed is not at target yet, so the sentinel must not
	// even be price-checked.
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if ys.buys != 0 {
		t.Fatalf("tick 1: the sentinel must not buy before the scouting seed is complete, got %d buys", ys.buys)
	}
	if spies.probes.buys != probeTarget {
		t.Fatalf("tick 1: the scouting seed itself must reach target in one tick, got %d buys", spies.probes.buys)
	}

	// Tick 2: the scouting seed's buys have synced into the observation. The sentinel now buys.
	spies.observer.obs.ProbeCount = probeTarget
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if ys.buys != 1 {
		t.Fatalf("tick 2: expected the sentinel to buy once the scouting seed is at target, got %d buys", ys.buys)
	}

	// Tick 3: the fresh buy has synced into the observation as an unparked sentinel. Positioning fires.
	spies.observer.obs.YardSentinelSymbol = "SENTINEL-1"
	ys.docked = false
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if ys.parkCalls != 1 {
		t.Fatalf("tick 3: expected one positioning attempt, got %d", ys.parkCalls)
	}

	// Tick 4: the sentinel is now parked. GATE begins (construction under way, not yet complete) —
	// the sentinel must survive untouched through the whole GATE phase: no re-buy, no re-position.
	ys.docked = true
	spies.observer.obs.YardSentinelParked = true
	spies.observer.obs.GateSite = "X1-HQ-GATE"
	spies.observer.obs.ConstructionStarted = true
	spies.observer.obs.ConstructionPercent = 40
	for tick := 4; tick <= 8; tick++ {
		if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
			t.Fatalf("gate tick %d: %v", tick, err)
		}
	}
	if ys.buys != 1 || ys.parkCalls != 1 {
		t.Fatalf("the sentinel must sit untouched through GATE, got buys=%d park_calls=%d", ys.buys, ys.parkCalls)
	}
	if ys.releaseCalls != 0 {
		t.Fatalf("GATE must never release the sentinel — only EXPANSION does, got %d release calls", ys.releaseCalls)
	}

	// The gate completes: EXPANSION. Bootstrap's own half of the hand-off fires — the release.
	spies.observer.obs.ConstructionComplete = true
	spies.observer.obs.ConstructionPercent = 100

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("expansion tick: %v", err)
	}
	if res.Phase != PhaseExpansion {
		t.Fatalf("expected the derived phase to be EXPANSION, got %s", res.Phase)
	}
	if ys.releaseCalls != 1 || ys.releasedSymbol != "SENTINEL-1" {
		t.Fatalf("EXPANSION must release the sentinel exactly once, got calls=%d symbol=%q", ys.releaseCalls, ys.releasedSymbol)
	}
	if ys.releasedReason == "" {
		t.Fatalf("the release must carry a reason for the audit trail, got empty")
	}
	if !res.Done {
		t.Fatalf("the coordinator must reach its terminal exit on the same tick the hand-off confirms")
	}
	// No second buy was ever attempted across the whole run — the one-shot property holds end to end.
	if ys.buys != 1 {
		t.Fatalf("exactly one sentinel must ever be bought across the whole COLDSTART→EXPANSION run, got %d", ys.buys)
	}
}

// --- sentinelShipTypeNeed: the pure predicate behind the placement fix (sp-ltl75) ---
//
// The sentinel's placement must track whichever workstream is CURRENTLY buying, not the type it happened
// to be bought as. This pins the precedence in isolation, off the SAME counts the heartbeat itself
// reports (probe_target/probes, hauler_target/haulers) — no Observer, no handler, no acquirer needed.
func TestSentinelShipTypeNeed_TracksTheActiveWorkstream(t *testing.T) {
	// The hauler bar is the tick's RESOLVED hauler_target, so a retune redirects the sentinel to the yard
	// serving the new need on the next tick — the cases below state the target they run at.
	haulerTarget := defaultTargets().HaulerTarget
	cases := []struct {
		name     string
		target   int
		obs      Observation
		wantType string
		wantOK   bool
	}{
		{
			name:     "probes still short of target: the scouting seed's own asset",
			target:   haulerTarget,
			obs:      Observation{ProbeCount: probeTarget - 1},
			wantType: probeShipType,
			wantOK:   true,
		},
		{
			name:     "probes complete, haulers short of target: the contract-hauler ramp's asset",
			target:   haulerTarget,
			obs:      Observation{ProbeCount: probeTarget, Haulers: nil},
			wantType: haulerShipType,
			wantOK:   true,
		},
		{
			name:     "probes complete, haulers partially ramped: still the hauler ramp's asset",
			target:   haulerTarget,
			obs:      Observation{ProbeCount: probeTarget, Haulers: make([]HaulerSnapshot, haulerTarget-1)},
			wantType: haulerShipType,
			wantOK:   true,
		},
		{
			name:   "both ramps at target: nothing is currently being bought",
			target: haulerTarget,
			obs:    Observation{ProbeCount: probeTarget, Haulers: make([]HaulerSnapshot, haulerTarget)},
			wantOK: false,
		},
		{
			// TUNED UP mid-ramp: a fleet fully staffed at the old target is short again, so the sentinel
			// goes back to standing watch over a hauler yard on the very next tick.
			name:     "hauler_target tuned up past the owned fleet: the ramp needs a hull again",
			target:   haulerTarget + 1,
			obs:      Observation{ProbeCount: probeTarget, Haulers: make([]HaulerSnapshot, haulerTarget)},
			wantType: haulerShipType,
			wantOK:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotOK := sentinelShipTypeNeed(tc.obs, tc.target)
			if gotOK != tc.wantOK {
				t.Fatalf("ok: got %v, want %v", gotOK, tc.wantOK)
			}
			if gotOK && gotType != tc.wantType {
				t.Fatalf("shipType: got %q, want %q", gotType, tc.wantType)
			}
		})
	}
}
