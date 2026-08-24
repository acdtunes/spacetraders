package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

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

	// ParkedSlotViews returns every PARKED placement with the columns the scan
	// rotation paces on — the whitelist it watches, its smoothed spread and its
	// last ATTEMPT stamp — which the state-only QueuedSlot projection does not
	// carry, plus the separate last-DATA stamp the staleness gauge reports
	// (sp-zml2u).
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
	// everything.
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
	YardScan parkedsensing.YardCatalogReader
	// YardPresence is the shipyard-read budget, asked which yards sell a hull we
	// want at a price no read can reveal because we have nobody standing there.
	// (sp-fox5u). It is the SAME budget instance every shipyard reader draws from,
	// so the yards it names here are exactly the ones its own rotation ranks
	// highest and keeps failing to price.
	//
	// OPTIONAL, and deliberately not in the ready() check below. Unlike the three
	// yard READ ports above — whose absence would mean the fleet never learns what
	// a shipyard sells, a blind spot worth failing loudly for — an absent presence
	// port means hulls are not repositioned. It is inert rather than fatal.
	YardPresence parkedsensing.YardPresenceDemand
	Treasury     parkedsensing.TreasuryReader
	CargoSpend   parkedsensing.CargoSpendReader
	Purchaser    parkedsensing.ProbePurchaser
	Ships        parkedsensing.ParkedShipReader
	Fleet        parkedsensing.FleetTagger
	Mover        parkedsensing.ShipMover
	Gates        parkedsensing.GateNeighbours
	// GateRead is the DELIBERATE, bounded fetch-through jump-gate read — the pass that learns where a
	// system connects without waiting for a hull to fly there. It is a SECOND port beside Gates
	// rather than a widening of it, because Gates is a pure store read by contract and asked of every
	// known system on every tick.
	//
	// Deliberately NOT in the ready() check below: the rest of the tick must keep running on a
	// daemon whose gate resolver is absent, and the pass is inert until it is present. The daemon
	// wires it unconditionally.
	GateRead  parkedsensing.GateReader
	Uncharted parkedsensing.UnchartedCatalog
	SeedShip  parkedsensing.SeedCommander
	// Partitioner cuts a dark system's stops into per-hull charting tours. Absent, a
	// crew falls back to angular sectors, so it is NOT in ready() below.
	Partitioner parkedsensing.FleetPartitioner
	Scan        parkedsensing.MarketScanRunner
	SpreadOf    parkedsensing.SpreadObserver
	Home        HomeSystemReader
	Budget      BudgetRateReader
	// UnpricedPool is the sensing surge's work list: the charted systems the fleet
	// holds no price for. REQUIRED, and checked in ready() below rather than
	// nil-tolerated: a nil-tolerant pool read is what a dormant feature looks like —
	// the tick reports healthy forever while surplus probes stand idle against the
	// unpriced map. Held fail-closed and LOUD instead.
	UnpricedPool UnpricedSystemPool
	// Wave answers which regime the tick is in: probe buying pauses on the HEAVY wave so
	// the treasury can reach a hull's ask, and runs at full speed on the PROBE one. The
	// name is the whole answer, not a reserve — the field used to carry only the ask,
	// and a name that lies is how the next reader misuses it.
	//
	// OPTIONAL: nil is the PROBE wave, which is what a deployment with no heavy buyer
	// should see. A nil-tolerant reserve was byte-identical to "no hold"; a nil-tolerant
	// wave must be byte-identical to "nothing to save for", never to a pause.
	Wave parkedsensing.WaveReader
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
		p.YardCatalog != nil && p.YardRead != nil && p.YardScan != nil &&
		// The sensing surge's pool read, REQUIRED for the same reason (sp-zvywu).
		p.UnpricedPool != nil
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

// yardPresencePorts bundles the presence pass's surface: the budget that names
// the unpriceable yards, the ledger it re-tasks rows in, the ship reader that
// confirms a hull is standing still, and the post reader that says which hulls
// are not ours to take.
//
// NO PURCHASER AND NO TREASURY, and that omission is the RULINGS #4 guarantee
// stated in the wiring rather than only in the prose: this pass cannot buy a
// probe because it is handed nothing that could. The post reader is built here
// rather than memoised onto the port struct for the same reason the drain's is —
// it is handler state, not a per-player adapter.
func (p SensingEnginePorts) yardPresencePorts(posts SensingPostRepository) parkedsensing.YardPresencePorts {
	return parkedsensing.YardPresencePorts{
		Demand:      p.YardPresence,
		Ledger:      p.Ledger,
		Ships:       p.Ships,
		MannedHulls: mannedHulls{posts: posts},
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
		// The SAME budget instance the presence pass is handed, so the mover and
		// the buyer rank dark shipyards identically (sp-7qhum). Only its READ half
		// crosses here — YardDemandReader, not YardPresenceDemand — so the drain
		// cannot consume the reposition allowance the presence pass paces itself
		// with. nil-safe: an unwired budget orders the queue as it was ordered
		// before the term existed.
		YardDemand: p.YardPresence,
		// nil-safe: an unwired wave reader means the PROBE wave — no heavy buyer to save
		// for — never a paused drain.
		Wave:                  p.Wave,
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
		Partitioner: p.Partitioner,
		// The SAME stored-listing read the buy queue's memo makes, so staging can prefer a
		// yard we have EVIDENCE sells probes over one the trait fallback merely guessed at —
		// and never stage onto one the memo has already answered probe-less. Without this
		// line the engine below has no evidence to rank on and behaves as it did before.
		ListingMemo: p.ListingMemo,
		Screen: func(ctx context.Context, system string) (parkedsensing.ScreenResult, error) {
			return parkedsensing.ScreenSystem(ctx, screen, playerID, system, whitelist)
		},
	}
}
