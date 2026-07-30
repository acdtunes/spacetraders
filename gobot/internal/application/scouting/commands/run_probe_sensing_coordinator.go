package commands

// The probe-sensing coordinator is the fleet's ONE budgeted sensing loop. Its
// model is PARKED probes: a hull is bought for a waypoint, flown there once, and
// then stands still forever, scanning its own market on a rotation the
// coordinator paces against whatever API headroom the rest of the fleet leaves.
// Nothing tours. Steady-state sensing therefore costs navigation nothing at all
// — the only recurring spend is the scans themselves, which is the quantity this
// loop actually sizes.
//
// The coordinator owns no algorithm of its own. Every decision belongs to an
// engine in internal/application/parkedsensing, and this file is the composition
// root that gives each engine its ports, orders them within a tick, and reports
// what they did:
//
//	screen      — is this system worth watching, and which waypoints in it?
//	buy queue   — can we afford a hull for that placement, and buy it
//	placements  — fly the bought hulls out and stand them down on station
//	expansion   — push the frontier outward, and run charting seeds
//	scanner     — the single fleet-wide pacer that spends the scan budget
//
// The loop is idempotent and restart-safe (RULINGS #2): every decision is
// re-derived each tick from the durable sensing ledger (sensing_systems,
// sensing_slots) and the ships table. The only in-memory state is the scan
// rotation, which a restart rebuilds from the ledger on the first tick, and the
// emergency brake, which a restart resets to fully-released and which re-derives
// within a few ticks from live limiter pressure.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

const (
	// Config defaults (RULINGS #5: every operational number is container config,
	// filled here only when the launch config leaves it unset).
	defaultSensingTickSeconds = 30
	defaultSensingWaitLowMs   = 50   // limiter wait at or under this: the brake recovers
	defaultSensingWaitHighMs  = 1000 // limiter wait at or past this: the brake bites

	// defaultParkedProbeCap is the hard ceiling on probe hulls the engine may
	// own. It is deliberately far above any fleet we expect to build: parked
	// probes are bought one placement at a time behind the dynamic buy floor,
	// so the BINDING constraint on fleet size is money, not this number. The cap
	// is the backstop against a runaway placement plan, not the growth dial.
	defaultParkedProbeCap = 3000

	// defaultExpansionEnabled is the expansion engine's master switch, encoded
	// 1=on / 2=off. It is NOT a bool-as-0/1 because `tune <key> 0` means "revert
	// to the default" fleet-wide — a 0/1 encoding would make "off" unexpressible.
	defaultExpansionEnabled = 1
	// expansionDisabled is the sentinel that switches expansion off.
	expansionDisabled = 2

	// defaultTargetUtilPct is the share of the rate-limiter ceiling the whole
	// fleet aims to occupy, leaving the remainder as burst headroom.
	defaultTargetUtilPct = 92
	// defaultMinScanRateMilli is the floor the pacer is clamped up to, in
	// thousandths of a request per second (100 = 0.1 req/s). It is what
	// guarantees planner data never goes fully dark under pressure.
	defaultMinScanRateMilli = 100
	// defaultValueClampR is the ceiling on how much more attention the hottest
	// market may earn than the baseline.
	defaultValueClampR = 4
	// defaultInflightCap bounds concurrent scans, and with it how hard a slow
	// API can push back on the pacer.
	defaultInflightCap = 3
	// defaultCapitalMultiplierKMilli is how many MILLI-hours of the trading
	// fleet's cargo runway the probe buy floor holds back on top of the
	// immutable reserve. 2000 = 2h, preserving the prior default exactly.
	//
	// Milli-hours because sub-hour runway is the real operating range: at a
	// measured 1.8M/hr cargo spend, whole hours move the floor 1.8M per step,
	// which left no setting between "no probe is ever affordable" and "no
	// runway guard at all". 400 = 0.4h. Same convention as
	// defaultMinScanRateMilli above.
	defaultCapitalMultiplierKMilli = 2000
	// defaultCapexReserveCredits is the credits held back for ship capex the
	// operation has already committed to elsewhere.
	defaultCapexReserveCredits = 100_000
	// defaultQuartermasterCadenceSecs is a yard slot's re-read interval. It is a
	// FLOOR on the scan interval, never a target: hull prices move on their own
	// schedule, so the budget may slow a yard down but never speed it past this.
	defaultQuartermasterCadenceSecs = 3600

	// screenSweepBatch bounds how many PENDING systems one tick screens. A plain
	// constant, deliberately not a knob: it paces API bursts (an unresolved
	// market costs a remote fetch, and a catalog-unknown system costs a
	// paginated waypoint sweep), not economics. The backlog is not lost — it is
	// worked over more ticks, and every system left over is still PENDING.
	screenSweepBatch = 5

	// budgetWindow is the trailing window the API budget is measured over. It
	// matches the tracker's own retention, so the reading is never diluted by
	// time the tracker cannot answer for.
	budgetWindow = 5 * time.Minute

	// pacerGuardComponent labels the panic guard around the scan pacer goroutine.
	pacerGuardComponent = "parked-sensing-pacer:"
)

// defaultSensingWhitelist is the era-invariant goods whitelist: a market is
// worth observing for what it DEALS IN, never what it is currently worth —
// prices are volatile and would drop a crushed market right before it recovers.
func defaultSensingWhitelist() []string {
	return []string{
		"CLOTHING", "LAB_INSTRUMENTS", "FABRICS", "FOOD", "ADVANCED_CIRCUITRY",
		"MEDICINE", "EQUIPMENT", "URANITE", "MICROPROCESSORS", "SHIP_PLATING",
		"MACHINERY", "ELECTRONICS",
	}
}

// MarketDepthReader is the CUTOVER's census read: one row per (waypoint, good)
// from the market cache. It exists solely to enumerate the systems the fleet
// already has market rows for, so the one-time cutover can screen them offline
// instead of rediscovering the map through the API. The steady-state loop never
// touches it — the frontier propagates through the expansion engine. Satisfied
// by the GORM market repository.
type MarketDepthReader interface {
	MarketDepthRows(ctx context.Context, playerID int) ([]domainScouting.MarketDepthRow, error)
}

// SensingPostRepository is the CUTOVER's posts-table surface. The parked model
// declares no posts of its own — a probe stands on a waypoint, not in a system —
// so the only thing this coordinator does with the posts table is retire the
// touring model's rows once, at cutover, keeping the home post the bootstrap
// scout-post coordinator still mans. Satisfied by the GORM scout-post repository.
type SensingPostRepository interface {
	domainScouting.ScoutPostRepository
}

// expansionPhaseReader reports whether the bootstrap-derived lifecycle phase is
// EXPANSION — the gate-built steady-state era demand-driven sensing belongs to.
// The phase is DERIVED from the live world (EXPANSION ⇔ the home jump-gate
// construction is COMPLETE), never read from a stored enum or a running
// container: bootstrap EXITS after its hand-off, so there is nothing to ask. An
// error means the phase could not be read, and this coordinator treats that
// FAIL-CLOSED. expansion.BootstrapExpansionPhaseReader satisfies it — the same
// reader the probe-buyer fleet gates on, so the two can never disagree about
// which era it is.
type expansionPhaseReader interface {
	InExpansion(ctx context.Context, playerID shared.PlayerID) (bool, error)
}

// HomeSystemReader resolves the player's headquarters system from the DATABASE,
// never the API. The cutover uses it to decide which single scout post survives
// the retirement of the touring model, so a read that went to the network would
// make an irreversible bulk delete depend on a call that can fail.
type HomeSystemReader interface {
	HomeSystem(ctx context.Context, playerID int) (string, error)
}

// BudgetRateReader is the coordinator's view of the shared API budget: how much
// of the rate-limiter ceiling everyone else is already using. It is a port
// rather than a direct read because the ceiling and the event tracker both live
// in the adapter layer, which the application layer must not import.
//
// The two rates are measured over the same trailing window and answer different
// questions. NonSensingRate is what the sensing residual is subtracted FROM;
// ChartingRate is what the pacer concedes to charting out of that residual. A
// reader that conflated them would double-count charting and walk the budget
// downward tick by tick.
type BudgetRateReader interface {
	// NonSensingRate is the observed req/s of every source that is neither
	// scanning nor charting.
	NonSensingRate(window time.Duration) float64
	// ChartingRate is the observed req/s spent on charting.
	ChartingRate(window time.Duration) float64
	// CeilingReqPerSec is the hard sustained rate-limiter ceiling.
	CeilingReqPerSec() float64
}

// SensingLedger is the durable placement ledger, as the whole engine sees it: the
// union of the narrow slices the engines declare, plus the one read this
// coordinator makes on its own behalf.
//
// The engines keep their surfaces disjoint on purpose (the screen cannot spend,
// the pacer cannot transition, the placement machine cannot count the fleet), and
// that discipline is preserved here — each engine is still handed only its own
// slice. This union exists so ONE adapter satisfies all of them, not so any
// engine gains reach.
//
// Every slice is listed even where its methods are a subset of another's — the
// reaper's three are all in ExpandLedger — because the list is what makes the
// requirement compile-time rather than incidental: narrowing one engine's
// interface must not silently take a method another still depends on.
type SensingLedger interface {
	parkedsensing.SlotLedger
	parkedsensing.BuyLedger
	parkedsensing.PlacementLedger
	parkedsensing.ScanLedger
	parkedsensing.ExpandLedger
	parkedsensing.ReapLedger

	// ParkedSlotViews returns every PARKED placement with the three columns the
	// scan rotation paces on — the whitelist it watches, its smoothed spread and
	// its last scan stamp — which the state-only QueuedSlot projection does not
	// carry.
	ParkedSlotViews(ctx context.Context, playerID int) ([]parkedsensing.SensingSlotView, error)
}

// SensingEnginePorts is the parked-probe engine's entire outbound surface,
// injected as one struct rather than as a dozen setters: the ports are wired
// together or not at all, and a half-wired engine is a wedge rather than a
// degraded mode (a coordinator that could screen but not buy would plan
// placements forever and fill none).
//
// It is built PER PLAYER (see SensingEnginePortsFactory). Two of the reads —
// the shipyard inventory behind ListProbeYards, and the catalog sweep stamp
// behind CatalogKnown — sit in player-scoped tables while their port signatures
// carry no player, so the player has to be bound into the adapter. This handler
// is a registered singleton serving every player's ticks, so binding it once at
// wiring time would answer every player's questions with the first player's rows.
type SensingEnginePorts struct {
	Ledger    SensingLedger
	Waypoints parkedsensing.WaypointCatalog
	// ListingMemo answers what a previous shipyard read persisted about a yard's
	// stock, so the drain can skip yards already known to sell no probe without
	// spending a live quote on each one, every tick. OPTIONAL: nil quotes
	// everything, which is the pre-memo behaviour.
	ListingMemo  parkedsensing.ProbeListingMemo
	MarketGoods  parkedsensing.MarketGoodsReader
	RemoteMarket parkedsensing.RemoteMarketFetcher
	// YardCatalog enumerates the charted shipyards whose catalogue we do not hold
	// — the free pass's work list, re-derived from the store every tick.
	YardCatalog parkedsensing.YardCatalogFrontier
	// YardRead is the free pass's reader: it learns what a shipyard SELLS with no
	// hull anywhere near it. Billed to the charting envelope, like the screen's
	// remote market fetch.
	YardRead parkedsensing.YardCatalogReader
	// YardScan is the SAME read taken from a parked probe's own turn, which is what
	// prices the yards we occupy and what finally records the shipyard under a
	// market sensor's feet. Billed to the scanning envelope, like the market scan
	// it rides.
	YardScan   parkedsensing.YardCatalogReader
	Treasury   parkedsensing.TreasuryReader
	CargoSpend parkedsensing.CargoSpendReader
	Purchaser  parkedsensing.ProbePurchaser
	Ships      parkedsensing.ParkedShipReader
	Fleet      parkedsensing.FleetTagger
	Mover      parkedsensing.ShipMover
	Gates      parkedsensing.GateNeighbours
	// GateRead is the DELIBERATE, bounded fetch-through jump-gate read — the pass that learns where a
	// system connects without waiting for a hull to fly there. It is a SECOND port beside Gates
	// rather than a widening of it, because Gates is a pure store read by contract and asked of every
	// known system on every tick.
	//
	// Deliberately NOT in the ready() check below, for the same reason OffGate is not: the rest of
	// the tick must keep running on a daemon whose gate resolver is absent, and the pass is inert
	// until it is present. The daemon wires it unconditionally.
	GateRead  parkedsensing.GateReader
	Uncharted parkedsensing.UnchartedCatalog
	// OffGate is the warp-expansion slice: the ports that raise explorer demand onto the fleet
	// autosizer's buy bridge and warp an explorer past a sealed gate frontier. Deliberately NOT in
	// the ready() check below — the gate passes must keep running on a daemon whose off-gate
	// collaborators are absent, and the slice is inert until all four are present.
	OffGate  parkedsensing.OffGatePorts
	SeedShip parkedsensing.SeedCommander
	Scan     parkedsensing.MarketScanRunner
	SpreadOf parkedsensing.SpreadObserver
	Home     HomeSystemReader
	Budget   BudgetRateReader
	// HeavyReserve holds treasury back for the next heavy so probe buying stands down
	// while one accumulates (sp-fwk8z). OPTIONAL: nil is byte-identical to no reserve,
	// which is what a deployment without the fleet autosizer should see.
	HeavyReserve parkedsensing.HeavyReserveReader
}

// SensingEnginePortsFactory builds one player's engine surface. The daemon wires
// a factory rather than a struct so the player-scoped adapters are bound to the
// player whose tick is actually running.
type SensingEnginePortsFactory func(playerID int) SensingEnginePorts

// wired reports whether every port the reconcile depends on is present. It is
// checked once per tick and fails the whole tick CLOSED, loudly: a nil port
// discovered halfway through a tick would leave the ledger half-advanced.
func (p SensingEnginePorts) wired() bool {
	return p.Ledger != nil && p.Waypoints != nil && p.MarketGoods != nil && p.RemoteMarket != nil &&
		p.Treasury != nil && p.CargoSpend != nil && p.Purchaser != nil && p.Ships != nil &&
		p.Fleet != nil && p.Mover != nil && p.Gates != nil && p.Uncharted != nil &&
		p.SeedShip != nil && p.Scan != nil && p.SpreadOf != nil && p.Home != nil && p.Budget != nil &&
		// The shipyard reads are REQUIRED, not optional-injection like ListingMemo or
		// HeavyReserve beside them. A nil-tolerant yard read is what a dormant feature
		// looks like: the engine would tick along reporting healthy while never learning
		// what a single shipyard sells, which is precisely the blind spot these ports
		// exist to close. Held fail-closed and LOUD instead.
		p.YardCatalog != nil && p.YardRead != nil && p.YardScan != nil
}

// --- engine port bundles ------------------------------------------------------
//
// Each engine is handed its OWN narrow slice, cut from the single injected
// surface. The bundles are what keep the engines' disjoint reach real at
// composition time rather than merely documented in their interfaces: the
// screen is given no way to spend, the placement machine no way to count the
// fleet, the pacer no way to transition a slot.

func (p SensingEnginePorts) screenPorts() parkedsensing.ScreenPorts {
	return parkedsensing.ScreenPorts{
		Waypoints:    p.Waypoints,
		MarketGoods:  p.MarketGoods,
		RemoteMarket: p.RemoteMarket,
		Ledger:       p.Ledger,
	}
}

// yardCatalogPorts bundles the free shipyard-catalogue sweep's surface. Two
// reads and nothing else: it can enumerate the yards nobody has asked about, and
// it can ask. It is handed no ledger, no purchaser and no mover — a discovery
// pass that could write a placement or spend a credit would be a different
// engine.
func (p SensingEnginePorts) yardCatalogPorts() parkedsensing.YardCatalogPorts {
	return parkedsensing.YardCatalogPorts{
		Frontier: p.YardCatalog,
		Catalog:  p.YardRead,
	}
}

// buyPorts bundles the drain's surface. It takes the container id rather than
// reading one off the (per-PLAYER, process-lifetime memoised) port struct: the
// purchasing hull's claim is written to ships.container_id under a foreign key to
// containers(id), so it must name the container that is actually driving THIS
// tick. A relaunched coordinator gets a fresh container id, and binding one into
// the memoised adapters would hand the database a row that no longer exists.
// mannedHulls satisfies parkedsensing.MannedHullReader over the scout-post
// repository, so the drain's foothold path can tell a hull that is merely parked
// on a market from one that is MANNING A POST somewhere.
//
// It reads every slot of every post — ScoutPost.MannedHulls covers the primary
// hull and the extra slots — because a hull in an extra slot is manning the post
// exactly as much as the primary one is, and enumerating only the primary would
// leave the multi-hull posts' hulls looking free to take.
type mannedHulls struct {
	posts SensingPostRepository
}

func (m mannedHulls) MannedHulls(ctx context.Context, playerID int) (map[string]bool, error) {
	posts, err := m.posts.ListActive(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list scout posts for the foothold guard: %w", err)
	}
	manned := make(map[string]bool, len(posts))
	for _, post := range posts {
		for _, hull := range post.MannedHulls() {
			manned[hull] = true
		}
	}
	return manned, nil
}

func (p SensingEnginePorts) buyPorts(claimOwnerContainerID string, posts SensingPostRepository) parkedsensing.BuyPorts {
	return parkedsensing.BuyPorts{
		Treasury:   p.Treasury,
		CargoSpend: p.CargoSpend,
		Purchaser:  p.Purchaser,
		Ledger:     p.Ledger,
		Yards:      p.Waypoints,
		Ships:      p.Ships,
		Fleet:      p.Fleet,
		// Same instance the yard lookup uses, so the two can never disagree about
		// what the stored inventory says.
		ListingMemo: p.ListingMemo,
		// The foothold path's two guard ports. Gates is the same topology store
		// expansion walks, so "how far may a hull be sent" is answered from one
		// place. The post reader is built here rather than memoised onto the port
		// struct for the same reason claimOwnerContainerID is passed: it is
		// handler state, not a per-player adapter.
		Gates:       p.Gates,
		MannedHulls: mannedHulls{posts: posts},
		// nil-safe: an unwired reserve means no hold-back, not a stalled drain.
		HeavyReserve:          p.HeavyReserve,
		ClaimOwnerContainerID: claimOwnerContainerID,
	}
}

// reapPorts bundles the stranded-claim reaper's surface. It is the narrowest
// slice cut here, and pointedly so: the reaper runs immediately before the drain
// and rewrites the same rows, so it is handed no purchaser, no treasury and no
// probe count — nothing that could turn a bookkeeping sweep into a spending path.
func (p SensingEnginePorts) reapPorts() parkedsensing.ReapPorts {
	return parkedsensing.ReapPorts{Ledger: p.Ledger}
}

func (p SensingEnginePorts) placementPorts() parkedsensing.PlacementPorts {
	return parkedsensing.PlacementPorts{
		Ledger: p.Ledger,
		Ships:  p.Ships,
		Mover:  p.Mover,
		Fleet:  p.Fleet,
	}
}

// expandPorts bundles the expansion engine's surface. Its Screen is a closure
// over the screen's own ports, the player and the whitelist — the values fixed
// for the life of a tick — so a finished charting seed can ask "what is this
// system worth?" without the expansion engine acquiring the screen's
// dependencies.
func (p SensingEnginePorts) expandPorts(playerID int, whitelist map[string]bool) parkedsensing.ExpandPorts {
	screen := p.screenPorts()
	return parkedsensing.ExpandPorts{
		Gates: p.Gates,
		// The deliberate fetch-through gate read. Gates above stays the pure per-tick store read;
		// this is the separate, bounded seam the gate-read pass spends API budget through.
		GateRead:    p.GateRead,
		Ledger:      p.Ledger,
		SeedShip:    p.SeedShip,
		Ships:       p.Ships,
		MarketGoods: p.MarketGoods,
		Yards:       p.Waypoints,
		Uncharted:   p.Uncharted,
		// The SAME stored-listing read the buy queue's memo makes, so staging can prefer a
		// yard we have EVIDENCE sells probes over one the trait fallback merely guessed at —
		// and never stage onto one the memo has already answered probe-less. Without this
		// line the engine below has no evidence to rank on and behaves as it did before.
		ListingMemo: p.ListingMemo,
		OffGate:     p.OffGate,
		Screen: func(ctx context.Context, system string) (parkedsensing.ScreenResult, error) {
			return parkedsensing.ScreenSystem(ctx, screen, playerID, system, whitelist)
		},
	}
}

// ParkedSensingRecorder is the metrics seam. Pure OBSERVATION (RULINGS #4): a
// recording miss must never touch a decision path, so every implementation is
// nil-safe and best-effort.
type ParkedSensingRecorder interface {
	RecordRate(playerID int, reqPerSec float64)
	RecordStaleness(playerID int, tier string, seconds float64)
	RecordSlots(playerID int, state string, count int)
}

// RunProbeSensingCoordinatorCommand launches the standing coordinator for a
// player. All knobs are launch-config keys (RULINGS #5); the zero value falls
// back to the documented default.
//
// The retired touring model's knobs are RETAINED as fields and IGNORED. They are
// not dead weight: restart recovery rebuilds this command from the persisted
// container config (RULINGS #2), and a config written by the old core still
// carries them. Removing the fields would not fail loudly — configReader.OptionalInt
// simply ignores keys it is not asked for — but keeping them documents that the
// old keys are known-and-inert rather than accidentally dropped.
type RunProbeSensingCoordinatorCommand struct {
	PlayerID    shared.PlayerID
	ContainerID string

	GoodsWhitelist []string
	TickSecs       int
	WaitLowMs      int
	WaitHighMs     int

	// ProbeCap is the hard ceiling on probe hulls the engine may own.
	ProbeCap int
	// ExpansionEnabled switches the expansion engine on or off, encoded
	// 1=on / 2=off. See defaultExpansionEnabled for why it is not 0/1.
	ExpansionEnabled int
	// TargetUtilPct is the share of the rate-limiter ceiling the fleet aims at.
	TargetUtilPct int
	// MinScanRateMilli is the pacer's floor, in thousandths of a request/sec.
	MinScanRateMilli int
	// ValueClampR bounds how much more attention the hottest market may earn
	// than the baseline. 1 flattens the weighting entirely.
	ValueClampR int
	// InflightCap bounds concurrent scans.
	InflightCap int
	// CapitalMultiplierKMilli is how many MILLI-hours of cargo runway the buy
	// floor holds. 2000 = 2h, 400 = 0.4h.
	CapitalMultiplierKMilli int
	// CapexReserveCredits is the committed-capex reserve the buy floor adds.
	CapexReserveCredits int
	// QuartermasterCadence is a yard slot's re-read floor, in seconds.
	QuartermasterCadence int

	// --- retired: read by the old touring core, ignored by this one -----------

	DepthFloor               int64
	ProbeBudget              int
	SecondProbeThreshold     int
	PurchaseCooldownSecs     int
	FreshnessTargetSecs      int
	MaxSpendPerCycle         int
	SpendWindowSecs          int
	DiscoveryDeclaresPerTick int
}

// RunProbeSensingCoordinatorResponse reports reconcile progress. Because the
// loop is infinite it is only observed on context cancellation (shutdown).
type RunProbeSensingCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// RunProbeSensingCoordinatorHandler composes the parked-probe engines into one
// reconcile loop. It is a registered singleton (one instance serves every
// player's ticks), so the per-container scan rotation and emergency brake are
// held in maps behind a mutex.
type RunProbeSensingCoordinatorHandler struct {
	depthReader MarketDepthReader
	postRepo    SensingPostRepository
	fleetRepo   FleetReader
	pressure    domainScouting.PressureReader
	phase       expansionPhaseReader
	clock       shared.Clock

	// newPorts builds the engine's outbound surface for one player, wired as
	// one unit after construction (the codebase's optional-injection idiom).
	// An unwired coordinator holds every tick inert and says so.
	newPorts SensingEnginePortsFactory

	// liveConfig gives each tick a fresh view of the persisted container config,
	// so `spacetraders tune` takes effect on the NEXT tick rather than at the
	// next rebuild. A nil reader (or a failed snapshot) runs the tick on the
	// launch command — never a half-applied config.
	liveConfig liveconfig.Reader

	// captainEvents emits the coordinator error-loop event when a reconcile
	// pass fails with the identical error for DefaultStreakThreshold
	// consecutive ticks — under the wake model the captain event IS the
	// standing failure sensor. Optional-injection.
	captainEvents captain.EventRecorder

	// recorder publishes the sensing gauges. Optional; nil means metrics are off.
	recorder ParkedSensingRecorder

	// stall is the WRITE-ONLY stall-escalation seam (health.StallObserver): each tick reports
	// PROGRESS / IDLE / BLOCKED(reason) for the sensing pass and for the off-gate/expansion pass
	// separately, so a wedge stops looking identical to a quiet fleet. Its single method returns
	// nothing, so no sensing decision can read the streak it accumulates (RULINGS #2 — see
	// internal/application/health/stall.go).
	stall health.StallObserver

	// mu guards the per-container state against the singleton-handler
	// concurrency (many containers' ticks share one handler).
	mu sync.Mutex
	// scanners holds each container's scan rotation. Memory-only by design: the
	// rotation is rebuilt from the ledger by the first SyncMembership, and every
	// slot's last_scan_at survives in the ledger, so a restart resumes with its
	// pacing intact.
	scanners map[string]*parkedsensing.Scanner
	// brakes holds each container's emergency throttle. A restart resets it to
	// fully released, which re-derives within a few ticks from live pressure.
	brakes map[string]float64
	// cutoverDone latches the one-time cutover per container, so a ledger that
	// legitimately reads empty later (a fresh era) does not re-run the scout-post
	// retirement it has no rows to justify.
	cutoverDone map[string]bool
	// portsByPlayer memoises each player's engine surface.
	portsByPlayer map[int]SensingEnginePorts
	// pacersRunning records which containers currently have a live scan pacer.
	// It is what makes starting one IDEMPOTENT — see ensurePacer.
	pacersRunning map[string]bool

	// runPacer is the pacer's entry point, a seam only for tests: production
	// runs the scanner's own loop, and a test substitutes one it can make exit
	// on demand (the real loop returns only on ctx cancellation).
	runPacer func(ctx context.Context, scanner *parkedsensing.Scanner)
}

// NewRunProbeSensingCoordinatorHandler wires the coordinator. clock defaults to
// the real clock when nil (production). The engine ports are injected separately
// via SetEnginePortsFactory. phase is the EXPANSION gate — a REQUIRED guard,
// deliberately a constructor parameter rather than an optional setter: a nil
// reader holds the whole coordinator inert (surfaced loudly every tick), never
// silently open.
func NewRunProbeSensingCoordinatorHandler(
	depthReader MarketDepthReader,
	postRepo SensingPostRepository,
	fleetRepo FleetReader,
	pressure domainScouting.PressureReader,
	phase expansionPhaseReader,
	clock shared.Clock,
) *RunProbeSensingCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunProbeSensingCoordinatorHandler{
		depthReader:   depthReader,
		postRepo:      postRepo,
		fleetRepo:     fleetRepo,
		pressure:      pressure,
		phase:         phase,
		clock:         clock,
		scanners:      map[string]*parkedsensing.Scanner{},
		brakes:        map[string]float64{},
		cutoverDone:   map[string]bool{},
		portsByPlayer: map[int]SensingEnginePorts{},
		pacersRunning: map[string]bool{},
		runPacer:      func(ctx context.Context, scanner *parkedsensing.Scanner) { scanner.RunPacer(ctx) },
	}
}

// SetEnginePortsFactory wires the parked-probe engine's outbound surface, as a
// per-player factory. Leaving it unset holds every tick inert.
func (h *RunProbeSensingCoordinatorHandler) SetEnginePortsFactory(f SensingEnginePortsFactory) {
	h.newPorts = f
}

// portsFor resolves one player's engine surface, memoised so a tick does not
// rebuild a dozen adapters it built last tick. The factory is called at most
// once per player for the life of the process.
func (h *RunProbeSensingCoordinatorHandler) portsFor(playerID int) (SensingEnginePorts, bool) {
	if h.newPorts == nil {
		return SensingEnginePorts{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if ports, ok := h.portsByPlayer[playerID]; ok {
		return ports, true
	}
	ports := h.newPorts(playerID)
	h.portsByPlayer[playerID] = ports
	return ports, true
}

// SetLiveConfigReader wires the per-tick live view of the persisted container
// config. Leaving it unset keeps the loop launch-frozen.
func (h *RunProbeSensingCoordinatorHandler) SetLiveConfigReader(r liveconfig.Reader) {
	h.liveConfig = r
}

// SetEventRecorder wires the captain outbox for the reconcile error-loop event.
func (h *RunProbeSensingCoordinatorHandler) SetEventRecorder(rec captain.EventRecorder) {
	h.captainEvents = rec
}

// SetMetricsRecorder wires the sensing gauges. Observation only.
func (h *RunProbeSensingCoordinatorHandler) SetMetricsRecorder(rec ParkedSensingRecorder) {
	h.recorder = rec
}

// SetStallObserver wires the coordinator-stall escalation seam. Optional and nil-safe. The seam
// is write-only by type (its one method returns nothing), so wiring it cannot give any sensing
// decision something new to branch on.
func (h *RunProbeSensingCoordinatorHandler) SetStallObserver(o health.StallObserver) { h.stall = o }

// noteReconcile records one reconcile pass at the streak checkpoint: a nil err
// resets the streak; a non-nil err repeating identically for
// DefaultStreakThreshold passes emits the coordinator error-loop captain event.
// Edge-triggered and nil-safe on the recorder.
func (h *RunProbeSensingCoordinatorHandler) noteReconcile(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, errMon *health.Monitor, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if streak, crossed := errMon.Note("reconcile", msg); crossed {
		health.RecordErrorLoop(h.captainEvents, common.LoggerFromContext(ctx), cmd.ContainerID, cmd.PlayerID.Value(), "reconcile", err, streak)
	}
}

// sensingConfig is one tick's effective config, with every default resolved.
type sensingConfig struct {
	Whitelist               map[string]bool
	Tick                    time.Duration
	WaitLow, WaitHigh       time.Duration
	ProbeCap                int
	// ExpansionSpend is whether this coordinator may spend on hulls at all. It
	// feeds BOTH engines that can: the expansion pass, which asks other engines to
	// buy (a charting seed from the buy queue, an explorer from the autosizer), and
	// the buy queue itself, which is what actually pays for a coverage probe.
	//
	// FEEDING ONLY THE FIRST WAS THE DEFECT. It was `Expansion`, and the rename to
	// ExpansionSpend fixed half of it — the old name said the engine was off while
	// what the operator wanted off was the spending, and switching the whole engine
	// off cost the fleet its free frontier discovery. The other half was that
	// "spending" then reached one spender: the drain bought six probes a cycle with
	// the switch off, 907,545 credits' worth (sp-com1h). Both knobs now read this
	// one field. See parkedsensing.ExpandKnobs.SpendEnabled and
	// parkedsensing.BuyKnobs.SpendEnabled.
	ExpansionSpend          bool
	TargetUtilPct           int
	MinScanRateMilli        int
	ClampR                  int
	InflightCap             int
	CapitalMultiplierKMilli int
	CapexReserveCredits     int64
	QuartermasterCadence    time.Duration
}

// resolveSensingConfig resolves one tick's effective config from the launch
// command overlaid with the live snapshot: a zero/absent knob means the
// documented default, which is exactly the `tune <key> 0` revert.
//
// The two out-of-contract cases are treated differently on purpose:
//
//   - ZERO is the REVERT, and it is silent. `tune <key> 0` means "go back to the
//     documented default" fleet-wide, and an absent key resolves to 0 too — which
//     is the normal state of every default launch. Warning here would fire on
//     every tick of most containers.
//   - NEGATIVE is a MISWRITE, and it warns. The tune registry bounds every key,
//     so a negative can only arrive from a hand-edited config row — and two of
//     them are silently destructive if merely absorbed: a negative
//     min_scan_rate_milli flows straight through to a NEGATIVE sensing rate (the
//     pacer's floor becomes a ceiling), and a clamp below 1 collapses the
//     weighting's optimistic prior so every unmeasured slot degrades to hourly
//     scans. Both faults are invisible in behaviour — the rotation still runs —
//     so the warning is the only trace they would otherwise leave.
func resolveSensingConfig(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, live liveconfig.Snapshot) sensingConfig {
	goods := cmd.GoodsWhitelist
	if len(goods) == 0 {
		goods = defaultSensingWhitelist()
	}
	whitelist := make(map[string]bool, len(goods))
	for _, good := range goods {
		whitelist[good] = true
	}

	pick := func(key string, launch int) int {
		if v, ok := live.PositiveInt(key); ok {
			return v
		}
		return launch
	}
	warnNegative := func(key string, v, fallback int) {
		if v >= 0 {
			return
		}
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Probe sensing knob %s is negative (%d) and has been replaced with its default (%d) — a negative here degrades the scan rotation silently, so it is never honoured",
			key, v, fallback), map[string]interface{}{
			"action": "parked_sensing_knob_rejected",
			"knob":   key,
			"value":  v,
		})
	}

	c := sensingConfig{
		Whitelist:               whitelist,
		Tick:                    time.Duration(pick("tick_secs", cmd.TickSecs)) * time.Second,
		WaitLow:                 time.Duration(pick("wait_low_ms", cmd.WaitLowMs)) * time.Millisecond,
		WaitHigh:                time.Duration(pick("wait_high_ms", cmd.WaitHighMs)) * time.Millisecond,
		ProbeCap:                pick("probe_cap", cmd.ProbeCap),
		TargetUtilPct:           pick("target_util_pct", cmd.TargetUtilPct),
		MinScanRateMilli:        pick("min_scan_rate_milli", cmd.MinScanRateMilli),
		ClampR:                  pick("value_clamp_r", cmd.ValueClampR),
		InflightCap:             pick("inflight_cap", cmd.InflightCap),
		CapitalMultiplierKMilli: pick("capital_multiplier_k_milli", cmd.CapitalMultiplierKMilli),
		CapexReserveCredits:     int64(pick("capex_reserve_credits", cmd.CapexReserveCredits)),
		QuartermasterCadence:    time.Duration(pick("quartermaster_cadence_secs", cmd.QuartermasterCadence)) * time.Second,
	}

	// 1=on, 2=off. Anything else — including the absent-key 0 — is the default.
	c.ExpansionSpend = pick("expansion_enabled", cmd.ExpansionEnabled) != expansionDisabled

	if c.Tick <= 0 {
		c.Tick = defaultSensingTickSeconds * time.Second
	}
	if c.WaitLow <= 0 {
		c.WaitLow = defaultSensingWaitLowMs * time.Millisecond
	}
	if c.WaitHigh <= 0 {
		c.WaitHigh = defaultSensingWaitHighMs * time.Millisecond
	}
	if c.ProbeCap <= 0 {
		c.ProbeCap = defaultParkedProbeCap
	}
	if c.TargetUtilPct <= 0 {
		c.TargetUtilPct = defaultTargetUtilPct
	}
	if c.MinScanRateMilli <= 0 {
		warnNegative("min_scan_rate_milli", c.MinScanRateMilli, defaultMinScanRateMilli)
		c.MinScanRateMilli = defaultMinScanRateMilli
	}
	if c.ClampR < 1 {
		warnNegative("value_clamp_r", c.ClampR, defaultValueClampR)
		c.ClampR = defaultValueClampR
	}
	if c.InflightCap <= 0 {
		c.InflightCap = defaultInflightCap
	}
	if c.CapitalMultiplierKMilli < 0 {
		warnNegative("capital_multiplier_k_milli", c.CapitalMultiplierKMilli, defaultCapitalMultiplierKMilli)
		c.CapitalMultiplierKMilli = defaultCapitalMultiplierKMilli
	}
	if c.CapitalMultiplierKMilli == 0 && cmd.CapitalMultiplierKMilli == 0 {
		// 0 is a legitimate operator choice (hold back no cargo runway at all),
		// but it is indistinguishable from an absent key, so the documented
		// default wins — matching every other knob's revert semantics.
		//
		// This is exactly why milli-units matter rather than being cosmetic: in
		// whole hours the only sub-1h setting WAS 0, which this branch then
		// reverts to the default — so a fractional runway was unreachable, not
		// merely awkward. 400 (=0.4h) is a distinct, settable value.
		c.CapitalMultiplierKMilli = defaultCapitalMultiplierKMilli
	}
	if c.CapexReserveCredits < 0 {
		warnNegative("capex_reserve_credits", int(c.CapexReserveCredits), defaultCapexReserveCredits)
		c.CapexReserveCredits = defaultCapexReserveCredits
	}
	if c.CapexReserveCredits == 0 && cmd.CapexReserveCredits == 0 {
		c.CapexReserveCredits = defaultCapexReserveCredits
	}
	if c.QuartermasterCadence <= 0 {
		c.QuartermasterCadence = defaultQuartermasterCadenceSecs * time.Second
	}
	return c
}

// liveSnapshot takes this tick's view of the persisted config. A missing reader
// or a failed read yields a nil snapshot, which resolveSensingConfig reads as
// "no live overrides" and runs entirely on the launch command — the fail-safe
// launch behaviour, never a half-applied config (liveconfig.go).
func (h *RunProbeSensingCoordinatorHandler) liveSnapshot(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) liveconfig.Snapshot {
	if h.liveConfig == nil {
		return nil
	}
	snapshot, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID.Value())
	if err != nil {
		return nil
	}
	return snapshot
}

// Handle runs the reconcile loop until the context is cancelled, with the scan
// pacer running alongside it.
//
// The pacer is started ONCE, before the first tick, and lives for the whole
// container: it is a single long-lived rotation whose membership the reconcile
// refreshes, not a per-tick job. Cancelling ctx stops both — the pacer drains
// its in-flight workers on the way out.
func (h *RunProbeSensingCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunProbeSensingCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	cfg := resolveSensingConfig(ctx, cmd, h.liveSnapshot(ctx, cmd))
	result := &RunProbeSensingCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Parked-probe sensing coordinator starting (tick %s, probe cap %d, expansion spending %v — frontier discovery runs either way)", cfg.Tick, cfg.ProbeCap, cfg.ExpansionSpend), map[string]interface{}{
		"action":       "probe_sensing_start",
		"container_id": cmd.ContainerID,
	})

	// The pacer is NOT started here. It is started from the tick path
	// (ensurePacer), which is what makes it both single and self-healing — see
	// that function; the container runner can re-enter this Handle several times
	// for one container, and a pacer that dies takes the fleet's scanning with it.

	errMon := health.NewMonitor(health.DefaultStreakThreshold)

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		err := h.ReconcileOnce(ctx, cmd)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Parked-probe sensing reconcile failed: %v", err), nil)
		}
		h.noteReconcile(ctx, cmd, errMon, err)
		result.Ticks++

		// Re-resolved every tick so a tuned cadence takes effect on the next
		// sleep rather than at the next rebuild.
		cfg = resolveSensingConfig(ctx, cmd, h.liveSnapshot(ctx, cmd))
		select {
		case <-time.After(cfg.Tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// ReconcileOnce is one reconcile pass — the unit the tests drive directly.
//
// The stages run in dependency order (screen plans placements, the queue fills
// them, the placement machine flies them, expansion finds the next system to
// screen) and every stage's failure is COLLECTED rather than fatal. A tick that
// cannot read the treasury must still advance the hulls already flying and must
// still refresh the scan rotation — aborting on the first error would let one
// unreadable port dark the whole fleet's market data.
func (h *RunProbeSensingCoordinatorHandler) ReconcileOnce(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) error {
	// EXPANSION GATE. Parked sensing buys hulls and sizes its footprint against
	// the fleet's trading reach; before the home jump gate is built there is no
	// such reach, and bootstrap owns probe provisioning. Checked FIRST so a
	// pre-EXPANSION tick does nothing at all — no ledger read, no cutover, no buy.
	if !h.expansionReached(ctx, cmd) {
		// Correctly gated, not wedged: bootstrap owns probes before the home gate is built.
		// Reported as IDLE so this stays silent for as many ticks as it takes, and so a stall
		// streak from before the gate does not survive across the phase change.
		h.observeStall(ctx, cmd, sensingStallCoordinator, health.TickIdle())
		return nil
	}

	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID.Value()
	ports, built := h.portsFor(playerID)
	if !built || !ports.wired() {
		logger.Log("WARNING", "Parked-probe sensing held (fail-closed): the engine ports are not wired — a half-wired engine plans placements it can never fill", map[string]interface{}{
			"action": "parked_sensing_unwired",
		})
		// A permanent wedge that produces the same silence as a quiet fleet. The expansion key
		// is deliberately left untouched: the pass did not run, so this tick is no evidence
		// about it either way, and a frozen streak neither escalates nor falsely clears.
		h.observeStall(ctx, cmd, sensingStallCoordinator, health.TickBlocked(stallReasonPortsUnwired,
			"the parked-sensing engine surface is incomplete — the tick holds fail-closed and can never fill a placement"))
		return nil
	}

	cfg := resolveSensingConfig(ctx, cmd, h.liveSnapshot(ctx, cmd))

	// Started here rather than in Handle, and re-checked on every tick: the
	// container runner can enter Handle several times for one container, and a
	// pacer that dies would otherwise take all parked-market scanning with it
	// while the heartbeat still looked healthy.
	h.ensurePacer(ctx, cmd, cfg, ports)

	// The budget is recomputed FIRST: expansion gates on the sensing residual and
	// the pacer runs at the floored rate, so both engines below need this tick's
	// numbers rather than the last tick's.
	budget := h.budgetInputs(cmd, cfg, ports)
	sensingRate := domainSensing.SensingRate(budget)
	pacerRate := domainSensing.PacerRate(budget)

	systems, err := ports.Ledger.Systems(ctx, playerID)
	if err != nil {
		// The tick cannot even see its own world. Reported before the return so an unreadable
		// ledger escalates instead of presenting as a fleet with nothing to do.
		h.observeStall(ctx, cmd, sensingStallCoordinator, health.TickBlocked(stallReasonLedgerUnreadable, err.Error()))
		return fmt.Errorf("failed to read the sensing ledger: %w", err)
	}

	var failures []error
	cutover, rotation := 0, 0
	// cutoverPending is the cutover's own trigger, named because the adoption
	// retry below gates on it too: while the cutover has not finished, adopting
	// orphans is still ITS job (sp-0gp21).
	cutoverPending := len(systems) == 0 && !h.cutoverAlreadyDone(cmd.ContainerID)
	if cutoverPending {
		count, cerr := h.cutover(ctx, cmd, cfg, ports)
		cutover = count
		if cerr != nil {
			// A partial cutover is durable and resumable — every write it made
			// stands — so the tick continues and the next one finishes the job.
			failures = append(failures, cerr)
		}
	}

	screened, serr := h.screenSweep(ctx, cmd, cfg, ports)
	if serr != nil {
		failures = append(failures, serr)
	}

	// The free shipyard-catalogue sweep, run on EVERY tick and gated on nothing.
	//
	// It sits outside the screen sweep deliberately. Screening only ever revisits
	// PENDING systems (an IN_SCOPE one is never re-screened, by design), so a yard
	// in a system we have already judged would never be reached from there — and
	// that is most of the map. This pass asks about yards, not systems, and its
	// work list shrinks to nothing on its own as the reads land.
	//
	// AFTER the screen so a system charted by this tick's sweep already has its
	// waypoint rows, and its yards are enumerable on this same tick rather than the
	// next one. BEFORE the drain because the drain's yard lookup reads the very
	// rows this pass writes.
	yardRep, yerr := parkedsensing.ReadYardCatalogues(ctx, ports.yardCatalogPorts(), playerID)
	if yerr != nil {
		failures = append(failures, yerr)
	}

	// BETWEEN THE SWEEP AND THE DRAIN, and both edges are the contract.
	//
	// After the sweep, so a verdict written by THIS tick is honoured by it: a
	// system the sweep has just restored to IN_SCOPE keeps its claim, which the
	// drain then re-works. Running the reaper first would read a stale map and
	// revert claims the sweep was about to justify.
	//
	// Before the drain, so a claim released this tick is a WANTED placement the
	// drain can fill immediately rather than one that waits a whole tick.
	//
	// The reaper re-reads the system table itself, and that RE-READ IS THE POINT.
	// Do not pass it the `systems` slice loaded at the top of this tick, however
	// much that looks like an obvious de-dup of a full-table read: that read
	// happens BEFORE the screening sweep, so reusing it would hand the reaper a
	// pre-sweep verdict map and silently restore exactly the stale-verdict
	// reaping the ordering above exists to prevent.
	reapRep, rerr := parkedsensing.ReapStrandedClaims(ctx, ports.reapPorts(), playerID, 0)
	if rerr != nil {
		failures = append(failures, rerr)
	}

	// The reaper's sibling, and also BEFORE the drain — for the same reason. A
	// hull adopted this tick is a parked SPARE the buy queue can re-task instead
	// of buying, so running it after the drain would spend money on a probe we
	// already own and had just finished recording (sp-0gp21).
	//
	// GATED ON THE CUTOVER, NOT THE PHASE. Before the cutover a scout-tagged hull
	// is not an orphan at all — the scout posts still stand and the scout-post
	// coordinator is actively manning them and drawing from its idle pool — so a
	// pass running then would take hulls out from under a LIVE coordinator. The
	// cutover is precisely the event that turns "scout-tagged hull" into "orphan
	// we failed to adopt", and while it is still pending (including while it
	// retries after a failure) adoption remains its job.
	// AFTER ADOPTION AND BEFORE THE DRAIN, and both edges are money.
	//
	// After adoption, so a hull adoption can absorb WHERE IT STANDS is absorbed
	// there rather than flown somewhere. Adoption's in-place fill costs a row
	// write and no movement; running the dispatch first would fly a hull to a
	// placement adoption was about to fill for free, and leave the placement
	// under that hull's feet still open. The dispatch re-reads the ledger, so
	// everything adoption just did is visible to it.
	//
	// Before the drain, and this is the whole economic point: a placement filled
	// here is a placement the drain does NOT buy a hull for. Running it after the
	// drain would spend money on a probe we already own and had standing idle —
	// the same argument that puts adoption before the drain (sp-0gp21), except
	// that here the hull is not merely counted but actually put to work.
	//
	// GATED ON THE CUTOVER for adoption's reason exactly: before the cutover a
	// scout-tagged hull is not an orphan at all, the scout posts still stand, and
	// the scout-post coordinator is actively drawing from its idle pool. Flying
	// one then would take a hull out from under a LIVE coordinator.
	adopted, dispatchedOrphans := 0, 0
	if !cutoverPending {
		adopted = h.adoptStrandedProbes(ctx, cmd, ports, &failures)
		dispatchedOrphans = h.dispatchIdleOrphans(ctx, cmd, ports, &failures)
	}

	buyRep, berr := parkedsensing.DrainBuyQueue(ctx, ports.buyPorts(cmd.ContainerID, h.postRepo), playerID, buyKnobs(cfg), h.clock)
	if berr != nil {
		failures = append(failures, berr)
	}

	placeRep, perr := parkedsensing.AdvancePlacements(ctx, ports.placementPorts(), playerID, 0)
	if perr != nil {
		failures = append(failures, perr)
	}

	// Expansion is gated on the SENSING residual, never the pacer rate: the
	// emergency brake can legitimately drive the residual below the minimum scan
	// rate, and the pacer re-imposes that floor — so gating on the pacer rate
	// would make the brake invisible here and leave expansion charting away at
	// full tilt through a rate-limit storm (budget.go:82-116).
	expandRep, eerr := parkedsensing.AdvanceExpansion(ctx, ports.expandPorts(playerID, cfg.Whitelist), playerID, parkedsensing.ExpandKnobs{
		SpendEnabled:  cfg.ExpansionSpend,
		MinBudgetRate: float64(cfg.MinScanRateMilli) / 1000.0,
		Whitelist:     cfg.Whitelist,
	}, sensingRate)
	if eerr != nil {
		failures = append(failures, eerr)
	}

	views, verr := ports.Ledger.ParkedSlotViews(ctx, playerID)
	if verr != nil {
		failures = append(failures, fmt.Errorf("failed to read the parked scan rotation: %w", verr))
	} else {
		// The pacer runs at the FLOORED rate, so market data never goes fully
		// dark however hard the brake bites.
		scanner := h.scannerFor(cmd, cfg, ports)
		scanner.SyncMembership(h.stampCadence(views, cfg), pacerRate)
		rotation, _ = scanner.RotationSize()
		h.publish(ctx, playerID, pacerRate, views, ports)
	}

	h.heartbeat(ctx, cmd, cfg, heartbeat{
		sensingRate: sensingRate,
		pacerRate:   pacerRate,
		brake:       budget.BrakeFactor,
		cutover:     cutover,
		screened:    screened,
		adopted:     adopted,
		dispatched:  dispatchedOrphans,
		reap:        reapRep,
		buy:         buyRep,
		place:       placeRep,
		expand:      expandRep,
		rotation:    rotation,
		yard:        yardRep,
	})

	// The tick's three-way verdict, on the two keys that stall independently. Reported ONCE per
	// tick each — the streak IS the tick count — and derived purely from tallies this tick
	// already produced, so nothing here can influence what the tick did.
	h.observeStall(ctx, cmd, sensingStallCoordinator, sensingTickVerdict(sensingTickTally{
		cutover:    cutover,
		screened:   screened,
		adopted:    adopted,
		dispatched: dispatchedOrphans,
		rotation:   rotation,
		reap:       reapRep,
		buy:        buyRep,
		place:      placeRep,
		expand:     expandRep,
		failures:   len(failures),
	}))
	h.observeStall(ctx, cmd, expansionStallCoordinator, expansionStallVerdict(expandRep, eerr))

	return errors.Join(failures...)
}

// buyKnobs is the buy queue's economics for this tick, derived from the resolved
// config.
//
// A NAMED FUNCTION RATHER THAN A STRUCT LITERAL AT THE CALL SITE, so the one line
// that matters here is assertable. SpendEnabled carries the SAME
// `expansion_enabled` switch the expansion pass reads, and both engines need it
// because they spend through different doors: expansion stops ASKING other engines
// to buy, and this queue stops BUYING. Wiring only the first is precisely what let
// 25 probes and 907,545 credits go out while the switch read off — a correct gate,
// shipped unreached (sp-com1h). See sensing_expand_wiring_test.go.
func buyKnobs(cfg sensingConfig) parkedsensing.BuyKnobs {
	return parkedsensing.BuyKnobs{
		SpendEnabled: cfg.ExpansionSpend,
		ProbeCap:     cfg.ProbeCap,
		CapexReserve: cfg.CapexReserveCredits,
		KMilli:       cfg.CapitalMultiplierKMilli,
	}
}

// budgetInputs takes this tick's reading of the shared API budget and advances
// the emergency brake from live limiter pressure.
//
// The brake is per-container and multiplicative, so it is advanced exactly once
// per tick — reading it twice would double-brake, and the scan pacer deliberately
// has no pressure port of its own for the same reason (scanner.go's ScanPorts).
func (h *RunProbeSensingCoordinatorHandler) budgetInputs(cmd *RunProbeSensingCoordinatorCommand, cfg sensingConfig, ports SensingEnginePorts) domainSensing.BudgetInputs {
	wait := time.Duration(0)
	if h.pressure != nil {
		wait = h.pressure.Current(h.clock.Now())
	}

	h.mu.Lock()
	brake := domainSensing.ApplyBrake(h.brakes[cmd.ContainerID], wait, cfg.WaitLow, cfg.WaitHigh)
	h.brakes[cmd.ContainerID] = brake
	h.mu.Unlock()

	return domainSensing.BudgetInputs{
		CeilingReqPerSec: ports.Budget.CeilingReqPerSec(),
		TargetUtilPct:    cfg.TargetUtilPct,
		MinScanRateMilli: cfg.MinScanRateMilli,
		NonSensingRate:   ports.Budget.NonSensingRate(budgetWindow),
		ChartingRate:     ports.Budget.ChartingRate(budgetWindow),
		BrakeFactor:      brake,
	}
}

// screenSweep re-screens the PENDING systems, bounded to screenSweepBatch per
// tick.
//
// PENDING is the ONLY verdict re-screened, and that is the whole cost model: a
// system judged IN_SCOPE has its placements and never needs judging again, and
// NO_WHITELIST is durable by design. Re-screening either would put the sweep's
// cost on the size of the known map rather than on the size of the frontier.
//
// A PENDING system whose waypoint CATALOG has never been swept is swept first,
// in-band. Without it the screen reads an unswept system's empty waypoint list
// as a fully-examined barren one — the same reading, opposite meaning — and the
// verdict it would record is durable AND makes the system a frontier propagation
// origin, so one wrong write-off walks outward across the map. The sweep is a
// paginated multi-call read metered under the charting envelope; it is bounded
// here by the batch above and happens once per system, and the pacer concedes
// charting's measured rate out of its own share (budgetInputs), so the spend is
// accounted rather than merely tolerated.
func (h *RunProbeSensingCoordinatorHandler) screenSweep(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, cfg sensingConfig, ports SensingEnginePorts) (int, error) {
	playerID := cmd.PlayerID.Value()
	pending, err := ports.Ledger.SystemsByVerdict(ctx, playerID, parkedsensing.VerdictPending)
	if err != nil {
		return 0, fmt.Errorf("failed to list systems awaiting screening: %w", err)
	}
	// LEAST-RECENTLY-SCREENED FIRST. This ordering is the whole fairness
	// property, because the batch below is a fixed cap over a queue that DOES
	// NOT DRAIN: a system still being charted screens back to PENDING, so it
	// stays in this list. Any order that is stable across ticks therefore gives
	// the list a PERMANENT head — the same batch re-screened forever, and
	// everything behind it never screened at all. Sorting by symbol did exactly
	// that: measured live, the five alphabetically-first PENDING systems were
	// the five most-recently-screened, 18 seconds old, while the alphabetic tail
	// had gone 12.2 hours untouched. An unscreened system is never judged.
	//
	// Because screened_at is restamped on EVERY verdict write (PENDING
	// included), screening a system moves it to the back, so this rotates: N
	// systems are fully covered in ceil(N/batch) ticks at unchanged cost per
	// tick.
	//
	// A NEVER-screened system sorts before every screened one. That is the
	// newly-discovered frontier, the case this sweep most needs to reach, and it
	// is why ScreenedAt is a pointer: NULL is answered here explicitly rather
	// than collapsing to the zero time and being ordered correctly by accident.
	//
	// The symbol tie-break makes the order TOTAL. sort.Slice is not stable, and
	// equal timestamps are readily produced, so without it the sweep's pick
	// would vary between runs on the same data — untestable, and starvation
	// creeps back the moment a group of systems shares a stamp.
	sort.Slice(pending, func(i, j int) bool {
		a, b := pending[i], pending[j]
		if (a.ScreenedAt == nil) != (b.ScreenedAt == nil) {
			return a.ScreenedAt == nil
		}
		if a.ScreenedAt != nil && !a.ScreenedAt.Equal(*b.ScreenedAt) {
			return a.ScreenedAt.Before(*b.ScreenedAt)
		}
		return a.System < b.System
	})

	screen := ports.screenPorts()
	screened := 0
	var failures []error
	for _, system := range pending {
		if screened >= screenSweepBatch {
			break
		}
		screened++

		known, kerr := ports.Waypoints.CatalogKnown(ctx, system.System)
		if kerr != nil {
			failures = append(failures, fmt.Errorf("failed to read whether the waypoint catalog of %q is known: %w", system.System, kerr))
			continue
		}
		if !known {
			if serr := ports.SeedShip.SyncWaypoints(ctx, playerID, system.System); serr != nil {
				// Named, not silent: a repeating sweep failure is otherwise
				// invisible and holds the system PENDING forever.
				common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to sweep the waypoint catalog of %s; it stays PENDING: %v", system.System, serr), map[string]interface{}{
					"action":        "parked_sensing_catalog_sweep_failed",
					"system_symbol": system.System,
				})
				continue
			}
		}

		if _, serr := parkedsensing.ScreenSystem(ctx, screen, playerID, system.System, cfg.Whitelist); serr != nil {
			failures = append(failures, serr)
		}
	}
	return screened, errors.Join(failures...)
}

// cutover retires the touring sensing model, once, on the first reconcile that
// finds an empty ledger. It reports how many scout posts it removed.
//
// THE ORDER IS THE WHOLE SAFETY PROPERTY, and it is driven by one fact: the
// re-fire trigger is an EMPTY sensing_systems table, and screening WRITES that
// table — every verdict, PENDING included. So the screen is the step that
// disarms the retry, and it therefore runs LAST, after everything irreversible
// has already committed:
//
//  1. Retire every scout post EXCEPT the home one. Home is kept because the
//     scout-post coordinator still mans it and bootstrap floor-protects it; the
//     rest are the touring model's and have no owner once this engine runs.
//  2. Adopt the orphaned probes those posts were manning as parked SPARE hulls
//     where they stand. Second, so "orphaned" is read against the posts that
//     actually survive rather than the ones about to be removed.
//  3. Screen every system the fleet already has market rows for, OFFLINE. The
//     map is already in the database; paying the API to rediscover it would cost
//     a full sweep's worth of calls for information we already hold.
//
// Screening first — the obvious reading order — is the bug this ordering exists
// to prevent. It commits the trigger-disarming write BEFORE the hulls are
// accounted for, so ANY failure after it (an unlistable posts table, a partial
// removal, an unreadable fleet, a crash) leaves scout-tagged hulls belonging to
// no post and no slot, with the retry already dead. Those hulls are invisible to
// the probe-cap count, so the engine buys replacements for probes it already
// owns — permanently, with no path back (see adoptOrphanProbes).
//
// Both steps that now run first are IDEMPOTENT, which is what makes the retry
// safe rather than merely possible: ListActive after a partial removal returns
// what is left (and removing an absent post is explicitly not an error), and
// adoption skips any hull already carrying the sensing tag.
func (h *RunProbeSensingCoordinatorHandler) cutover(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, cfg sensingConfig, ports SensingEnginePorts) (int, error) {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID.Value()

	home, err := ports.Home.HomeSystem(ctx, playerID)
	if err != nil {
		// Without the home system every post looks retirable, including the one
		// the scout-post coordinator is actively manning. Refuse the cutover
		// rather than guess: nothing has been written, the ledger stays empty,
		// and the next tick retries from the top.
		return 0, fmt.Errorf("cutover refused: the home system is unreadable, so no scout post can be safely retired: %w", err)
	}

	var failures []error

	posts, err := h.postRepo.ListActive(ctx, playerID)
	if err != nil {
		// Nothing has been committed yet, so this really is a refusal: the
		// sensing ledger is still empty and the next tick re-enters here.
		return 0, fmt.Errorf("cutover refused: failed to list scout posts: %w", err)
	}
	removed := 0
	for _, post := range posts {
		if post.SystemSymbol == home {
			continue
		}
		if rerr := h.postRepo.Remove(ctx, playerID, post.SystemSymbol); rerr != nil {
			failures = append(failures, fmt.Errorf("failed to retire scout post %s: %w", post.SystemSymbol, rerr))
			continue
		}
		removed++
	}

	adopted := h.adoptOrphanProbes(ctx, cmd, ports, &failures)

	// The trigger-disarming write, last. A failure anywhere above has left the
	// ledger empty, so the whole cutover is re-run next tick over the idempotent
	// remains of this one.
	if len(failures) > 0 {
		return removed, errors.Join(append(failures,
			errors.New("cutover incomplete: the offline screen was held back so the empty sensing ledger re-triggers it next tick"))...)
	}
	screened := h.cutoverScreen(ctx, cmd, cfg, ports, &failures)
	if len(failures) > 0 {
		// The screen itself failed. The done-mark must NOT latch: it is in
		// memory, so latching it over an EMPTY ledger strands the engine until a
		// daemon restart — nothing else recovers it, because the steady-state
		// sweep re-screens only systems already carrying a PENDING row, and a
		// census that failed OUTRIGHT wrote none. That case is real and is what
		// this gate is for: a MarketDepthRows failure returns before a single
		// system is iterated, so nothing is written at all.
		//
		// A PARTIALLY screened ledger MOSTLY no longer needs the gate. It already
		// holds rows, so the cutover's own trigger will not re-enter whatever this
		// gate does — and usually it need not, because a system whose screen failed
		// carries a bare PENDING row of its own (sp-x665h, markAwaitingScreening),
		// which is exactly what the steady-state sweep re-screens. Recovery is
		// SAME-TICK, not next-tick: the sweep runs later in this very reconcile,
		// so a system marked PENDING here is re-screened before the tick ends.
		//
		// The one system that stays uncovered is the one whose FALLBACK write also
		// failed — it has no row, so the sweep cannot see it either, and only
		// frontier propagation will reach it. That is rarer than the case this
		// paragraph used to claim was impossible, but it is not impossible.
		//
		// A legitimately EMPTY census is not a failure and does latch: screened
		// == 0 with no errors is a fleet that has simply not scanned anything
		// yet, and the expansion frontier is what grows the map from there.
		logger.Log("WARNING", fmt.Sprintf(
			"Parked-probe cutover screened %d system(s) before failing; the done-mark is withheld. A system whose screen failed carries a PENDING row the sweep picks up later in this same tick — except where the census read itself failed (nothing was written at all, so the empty ledger re-triggers the cutover next tick) or the fallback write ALSO failed (that system is named in its own error above and carries no row, so only frontier propagation reaches it)",
			screened), map[string]interface{}{
			"action":           "parked_sensing_cutover_screen_failed",
			"systems_screened": screened,
		})
		return removed, errors.Join(append(failures,
			errors.New("cutover incomplete: the offline screen did not finish"))...)
	}

	h.markCutoverDone(cmd.ContainerID)
	logger.Log("INFO", fmt.Sprintf(
		"Parked-probe cutover: screened %d system(s) offline, retired %d scout post(s) (home %s kept), adopted %d orphaned probe(s) as spares",
		screened, removed, home, adopted), map[string]interface{}{
		"action":           "parked_sensing_cutover",
		"systems_screened": screened,
		"posts_removed":    removed,
		"home_system":      home,
		"probes_adopted":   adopted,
	})
	// Every failure path returned above, so `failures` is necessarily empty here
	// and there is nothing to join — returning nil says that outright rather than
	// leaving a reader to check.
	return removed, nil
}

// cutoverScreen screens every system the market cache already knows about,
// without touching the API.
//
// The offline guarantee is enforced by SUBSTITUTION, not by convention: the
// screen's remote gap-fill port is replaced with one that refuses, so a market
// the cache cannot answer for leaves its system PENDING (verdictFor requires
// every market to resolve before recording the durable rejection) instead of
// firing a fetch. The steady-state sweep re-screens those PENDING systems with
// the real fetcher wired.
func (h *RunProbeSensingCoordinatorHandler) cutoverScreen(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, cfg sensingConfig, ports SensingEnginePorts, failures *[]error) int {
	playerID := cmd.PlayerID.Value()
	rows, err := h.depthReader.MarketDepthRows(ctx, playerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to read the market census for the cutover screen: %w", err))
		return 0
	}

	seen := make(map[string]bool, len(rows))
	systems := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.System == "" || seen[row.System] {
			continue
		}
		seen[row.System] = true
		systems = append(systems, row.System)
	}
	sort.Strings(systems)

	offline := ports.screenPorts()
	offline.RemoteMarket = offlineMarketFetcher{}

	screened := 0
	for _, system := range systems {
		if _, serr := parkedsensing.ScreenSystem(ctx, offline, playerID, system, cfg.Whitelist); serr != nil {
			*failures = append(*failures, serr)
			markAwaitingScreening(ctx, ports, playerID, system, failures)
			continue
		}
		screened++
	}
	return screened
}

// markAwaitingScreening leaves a bare PENDING row for a system whose screen
// failed, so the steady-state sweep adopts it.
//
// WITHOUT THIS ROW THE SYSTEM FALLS THROUGH EVERY RECOVERY PATH. ScreenSystem
// writes its sensing_systems row LAST and only on full success, so a failure
// mid-census leaves that system with no row while its siblings have theirs. The
// cutover's own re-fire trigger is an EMPTY ledger, which the siblings' rows
// disarm; and the steady-state sweep re-screens only systems that ALREADY carry
// a PENDING row, which this one does not. What is left is frontier propagation
// happening to reach it, which depends on the map's topology rather than on
// anything we control.
//
// THE ROW ASSERTS ONLY WHAT WE KNOW, WHICH IS NOTHING. Symbol and verdict, and
// no third thing: a depth or an uncharted count invented here would be a
// measurement we never took, and PENDING already means exactly "nobody has
// judged this yet". It is the same shape the expansion engine writes for a
// newly discovered frontier neighbour, and it is read the same way.
//
// It goes through UpsertSystem, the verdict-scoped writer, whose column list
// structurally excludes seed_ship and seed_state — so this can never clear a
// charting errand, however it is called (sensingSystemUpdateColumns).
//
// A fallback that ITSELF fails is collected rather than swallowed and rather
// than fatal: there is nothing further to try for this system, and the systems
// after it in the census are still perfectly screenable.
func markAwaitingScreening(ctx context.Context, ports SensingEnginePorts, playerID int, system string, failures *[]error) {
	if uerr := ports.Ledger.UpsertSystem(ctx, playerID, parkedsensing.SystemRecord{
		System:  system,
		Verdict: parkedsensing.VerdictPending,
	}); uerr != nil {
		*failures = append(*failures, fmt.Errorf(
			"failed to record %q as awaiting screening after its cutover screen failed (it now carries no row, so only frontier propagation can reach it): %w",
			system, uerr))
	}
}

// offlineMarketFetcher is the cutover's stand-in for the API gap fill: it
// refuses every call, which screenMarkets reads as "this market did not
// resolve" and which leaves the system PENDING rather than rejected.
type offlineMarketFetcher struct{}

// errOfflineScreen names the refusal in the one place it could otherwise look
// like a real outage.
var errOfflineScreen = errors.New("cutover screens from the local market cache only; no remote market fetch is made")

func (offlineMarketFetcher) FetchGoods(context.Context, int, string, string) ([]string, error) {
	return nil, errOfflineScreen
}

// adoptOrphanProbes takes ownership of the probes the retired posts were
// manning, as parked SPARE slots where each hull already stands.
//
// A scout-tagged hull that no surviving post names is otherwise stranded: no
// coordinator drives it, and — the part that costs money — the probe cap counts
// hulls through the ledger, so an unrecorded probe makes the fleet read SMALLER
// than it is and authorises buying a replacement for a hull we already own.
// Adopting as SPARE also puts them straight to work: the buy queue prefers
// re-tasking a spare over buying, and expansion claims them as charting seeds.
func (h *RunProbeSensingCoordinatorHandler) adoptOrphanProbes(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, ports SensingEnginePorts, failures *[]error) int {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID.Value()
	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to list the fleet for probe adoption: %w", err))
		return 0
	}

	posts, err := h.postRepo.ListActive(ctx, playerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to re-list scout posts for probe adoption: %w", err))
		return 0
	}
	manned := make(map[string]bool, len(posts))
	for _, post := range posts {
		if post.AssignedHull != "" {
			manned[post.AssignedHull] = true
		}
	}

	// The SAME occupancy index the standing adoption retry uses
	// (probe_sensing_adoption.go), for the same reason and against the same
	// hazard — see the guard inside the loop. Seeded from the LEDGER rather than
	// built empty: this pass fires on a ledger the era scope reports as empty,
	// but rows can legitimately already exist within it, and a loop-local set
	// would only ever see what THIS loop wrote.
	holds, herr := ledgerHoldings(ctx, ports, playerID)
	if herr != nil {
		*failures = append(*failures, herr)
		return 0
	}

	adopted := 0
	for _, ship := range ships {
		if !ship.IsScoutType() || ship.DedicatedFleet() != freshnessScoutFleetTag {
			continue
		}
		if manned[ship.ShipSymbol()] {
			continue // still manning the home post the cutover kept
		}
		location := ship.CurrentLocation()
		if location == nil || location.Symbol == "" {
			continue // a hull we cannot place is a hull we must not record
		}
		// THE OCCUPANCY GUARD (sp-0gp21). UpsertSpareSlot's conflict set carries
		// assigned_ship, so a write at a waypoint that already holds a SPARE row
		// does not fail — it silently re-points that row at this hull. The hull it
		// displaced then holds the sensing tag with NO row anywhere, which is
		// UNRECOVERABLE: every adoption filter skips sensing_parked-tagged hulls,
		// the spare re-task and the seed claim both read FROM the ledger, and
		// DockedProbeAt will use such a hull as a purchasing buyer but never writes
		// a row naming it. It is invisible to CountOwnedProbes for good, so the cap
		// under-reads and buys a replacement for a probe we own (RULINGS #4) — with
		// no error and a healthy heartbeat. Two co-located idle probes at the home
		// shipyard is all it takes, on the one irreversible tick this pass ever
		// runs.
		//
		// Skipping is the right answer: the displaced hull would be lost, while a
		// skipped one stays untagged, unrecorded and therefore still recoverable.
		//
		// STILL KIND-BLIND after the key widened (sp-dpfp8), and deliberately: this
		// is the ONE irreversible tick, so it keeps the widest guard available and
		// leaves every judgement call to the passes that retry. See occupiedAt.
		//
		// sp-0eufi softened one clause above: adoption's filter is now an ALLOWLIST that INCLUDES
		// sensing_parked, so a tagged-but-unrecorded hull is no longer unrecoverable — the standing
		// pass picks it up. The displacement is still not worth risking here, and this remains a
		// plain skip: this is the ONE irreversible tick, while the standing pass retries every tick
		// and, unlike this path, knows how to fill a hull-less placement in place.
		if holds.occupiedAt(location.Symbol) {
			continue
		}

		// RECORD BEFORE TAGGING, for the reason recordPurchase gives in the buy
		// queue: the two writes are ordered by what a failure between them costs.
		// Recorded-but-untagged leaves a hull the probe cap COUNTS (it just is
		// not yet claimed by this fleet), and the placement machine re-asserts
		// the tag idempotently the first time the spare is used. Tagged-but-
		// unrecorded is the opposite and is unrecoverable here: the tag makes the
		// hull skip the adoption filter on every retry, so it would stay
		// invisible to the cap forever and authorise buying a replacement for a
		// probe we already own.
		if uerr := ports.Ledger.UpsertSpareSlot(ctx, playerID, parkedsensing.SlotRecord{
			Waypoint:     location.Symbol,
			System:       location.SystemSymbol,
			Kind:         parkedsensing.SlotKindSpare,
			State:        parkedsensing.SlotStateParked,
			AssignedShip: ship.ShipSymbol(),
		}); uerr != nil {
			*failures = append(*failures, fmt.Errorf("failed to adopt probe %s as a spare: %w", ship.ShipSymbol(), uerr))
			continue
		}
		// The waypoint holds a SPARE row now, so a later co-located hull in this
		// same sweep is skipped rather than overwriting what was just written.
		// APPENDED, not assigned: any MARKET placement already indexed here is
		// still on the books and must stay visible to the rest of the sweep.
		holds.rows[location.Symbol] = append(holds.rows[location.Symbol], parkedsensing.QueuedSlot{
			Waypoint:     location.Symbol,
			System:       location.SystemSymbol,
			Kind:         parkedsensing.SlotKindSpare,
			State:        parkedsensing.SlotStateParked,
			AssignedShip: ship.ShipSymbol(),
		})

		// The tag write is BEST-EFFORT, and deliberately not a failure of the
		// cutover. It is the one write here that is not money-load-bearing: the
		// probe cap counts ledger ROWS, and the row is already written, so a
		// missing tag costs nothing the cap can see. The placement machine
		// re-asserts it idempotently the first time the spare is used — the same
		// reasoning recordPurchase gives for warning on its own tag failure
		// rather than failing the purchase over it.
		//
		// Escalating it would be actively worse now that a failed cutover
		// re-runs from the top: a persistently failing tag write would re-run
		// the whole cutover on every tick, forever, over work that is already
		// correctly recorded.
		if terr := ports.Fleet.AssignFleet(ctx, playerID, ship.ShipSymbol(), parkedsensing.SensingParkedFleetTag); terr != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"Adopted probe %s is recorded as a sensing spare but keeps its old fleet tag (the probe cap counts it; the placement machine re-tags it on first use): %v",
				ship.ShipSymbol(), terr), map[string]interface{}{
				"action":      "parked_sensing_adopt_tag_failed",
				"ship_symbol": ship.ShipSymbol(),
			})
		}
		adopted++
	}
	return adopted
}

// stampCadence projects the ledger's parked slots into the scan rotation,
// applying the quartermaster cadence to yard slots. The cadence is a knob and
// therefore resolved here rather than in the adapter, which reads columns only.
func (h *RunProbeSensingCoordinatorHandler) stampCadence(views []parkedsensing.SensingSlotView, cfg sensingConfig) []parkedsensing.SensingSlotView {
	out := make([]parkedsensing.SensingSlotView, 0, len(views))
	for _, view := range views {
		if view.Kind == parkedsensing.SlotKindYard {
			view.YardCadence = cfg.QuartermasterCadence
		}
		out = append(out, view)
	}
	return out
}

// ensurePacer starts the container's scan pacer unless one is already running.
// It is called from every reconcile, and both halves of that matter.
//
// IDEMPOTENT, because Handle can run more than once for a single container. The
// container runner re-sends the SAME command — same container id, same
// uncancelled context — after an error or a panic, up to MaxRestartAttempts, so
// a pacer launched from Handle would be launched again on each retry. Two pacers
// popping one heap issue scans at twice the rate the budget arithmetic computed,
// and the heartbeat cannot show it: it reports the rate it HANDED to the
// rotation, not the rate being spent. The fleet would simply overrun its share
// of the rate limiter with nothing anywhere saying so.
//
// SELF-HEALING, because the panic guard around the pacer suppresses and returns
// rather than restarting. A single panic would otherwise stop all parked-market
// scanning for the life of the container while every heartbeat still reported a
// healthy computed rate — the failure would surface only as market data ageing
// without bound, hours later, on the staleness gauge. Re-checking here converts
// that into a one-tick outage with a loud line naming it.
func (h *RunProbeSensingCoordinatorHandler) ensurePacer(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, cfg sensingConfig, ports SensingEnginePorts) {
	scanner := h.scannerFor(cmd, cfg, ports)

	h.mu.Lock()
	if h.pacersRunning[cmd.ContainerID] {
		h.mu.Unlock()
		return
	}
	h.pacersRunning[cmd.ContainerID] = true
	run := h.runPacer
	h.mu.Unlock()

	go func() {
		defer h.pacerStopped(ctx, cmd)
		supervise.Guard(pacerGuardComponent+cmd.ContainerID, func() { run(ctx, scanner) })
	}()
}

// pacerStopped releases the container's pacer slot and, when the coordinator is
// still meant to be running, reports the death loudly.
//
// A cancelled context is an ordinary shutdown and says nothing. Anything else
// means the pacer returned or panicked while the fleet still expected it to be
// scanning, which is exactly the silent failure the re-check above exists to
// bound — so it is logged at ERROR with the relaunch stated, rather than left to
// be inferred from a staleness gauge.
func (h *RunProbeSensingCoordinatorHandler) pacerStopped(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) {
	h.mu.Lock()
	delete(h.pacersRunning, cmd.ContainerID)
	h.mu.Unlock()

	if ctx.Err() != nil {
		return // the coordinator is shutting down; the pacer stopping is correct
	}
	common.LoggerFromContext(ctx).Log("ERROR", fmt.Sprintf(
		"Parked-probe scan pacer for %s stopped while the coordinator is still running — every parked market has stopped being scanned; the next reconcile relaunches it",
		cmd.ContainerID), map[string]interface{}{
		"action":       "parked_sensing_pacer_died",
		"container_id": cmd.ContainerID,
	})
}

// pacerLive reports whether the container currently holds a pacer.
func (h *RunProbeSensingCoordinatorHandler) pacerLive(containerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pacersRunning[containerID]
}

// scannerFor returns the container's scan rotation, creating it on first use.
//
// InflightCap and ClampR bind at construction, so a tune of either applies at
// the coordinator's next rebuild rather than the next tick. That is deliberate:
// both shape the rotation's normalisation, and swapping them under a live pacer
// would re-pace every slot mid-flight. Every other knob is live.
func (h *RunProbeSensingCoordinatorHandler) scannerFor(cmd *RunProbeSensingCoordinatorCommand, cfg sensingConfig, ports SensingEnginePorts) *parkedsensing.Scanner {
	h.mu.Lock()
	defer h.mu.Unlock()

	if scanner, ok := h.scanners[cmd.ContainerID]; ok {
		return scanner
	}
	scanner := parkedsensing.NewScanner(cmd.PlayerID.Value(), parkedsensing.ScanPorts{
		Scan:     ports.Scan,
		Ledger:   ports.Ledger,
		SpreadOf: ports.SpreadOf,
		// The scanning-tagged yard read, so a parked probe records the shipyard
		// under its feet on the same turn it reads the market there. It is the only
		// path that ever PRICES a yard we occupy.
		Yard: ports.YardScan,
	}, h.clock, parkedsensing.ScanKnobs{
		InflightCap: cfg.InflightCap,
		ClampR:      cfg.ClampR,
	})
	h.scanners[cmd.ContainerID] = scanner
	return scanner
}

// cutoverAlreadyDone reports whether this container has already cut over.
//
// Named for what it RETURNS. It gates an irreversible bulk delete, and the
// previous name said "pending" while returning "done" — so the call site read as
// the opposite of what it did.
func (h *RunProbeSensingCoordinatorHandler) cutoverAlreadyDone(containerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cutoverDone[containerID]
}

func (h *RunProbeSensingCoordinatorHandler) markCutoverDone(containerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cutoverDone[containerID] = true
}

// expansionReached reports whether the bootstrap-derived phase is EXPANSION,
// naming the honest no-work reason when it is not (never a silent stall).
// FAIL-CLOSED on every unverifiable input (RULINGS #4): an unwired reader
// (mis-wire) and a read error both hold the coordinator inert, each with its own
// loud line so the wedge is visible on the first look.
func (h *RunProbeSensingCoordinatorHandler) expansionReached(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) bool {
	logger := common.LoggerFromContext(ctx)
	if h.phase == nil {
		logger.Log("WARNING", "Probe sensing held (fail-closed): no bootstrap-phase reader wired — the EXPANSION phase cannot be verified, and a gate never defaults open", map[string]interface{}{
			"action": "probe_sensing_phase_unreadable",
		})
		return false
	}
	inExpansion, err := h.phase.InExpansion(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Probe sensing held (fail-closed): bootstrap phase unreadable — no sensing on an unknown phase: %v", err), map[string]interface{}{
			"action": "probe_sensing_phase_unreadable",
			"error":  err.Error(),
		})
		return false
	}
	if !inExpansion {
		logger.Log("INFO", "Probe sensing deferred: bootstrap phase pre-EXPANSION (jump-gate construction incomplete — the world is still in DATA/INCOME/GATE); parked-probe sensing runs only in EXPANSION, bootstrap provisions probes until then", map[string]interface{}{
			"action": "probe_sensing_phase_deferred",
		})
		return false
	}
	return true
}
