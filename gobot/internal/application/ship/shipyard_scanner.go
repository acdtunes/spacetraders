package ship

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// shipyardAPI is the narrow slice of the SpaceTraders API the shipyard scanner
// needs (the gategraph gateAPI idiom): the one live shipyard read. Satisfied by
// domainPorts.APIClient.
type shipyardAPI interface {
	GetShipyard(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.ShipyardData, error)
}

// waypointTraitReader checks a waypoint's IMMUTABLE trait (SHIPYARD) from the
// local cache WITHOUT an API call. It deliberately reads era-agnostic and
// TTL-agnostic: a physical trait never changes across eras and never goes stale,
// so a prior-era or long-unsynced row is still authoritative. Satisfied
// by *persistence.GormWaypointRepository.HasWaypointTrait.
type waypointTraitReader interface {
	HasWaypointTrait(ctx context.Context, waypointSymbol, trait string) (bool, error)
}

// ShipyardScanner piggybacks shipyard scans on scout market visits:
// when a scout is at a waypoint bearing the SHIPYARD trait, it reads the live
// shipyard (ship types + priced listings — full prices are visible because the
// scout IS at the waypoint) and persists the result to the shipyard-inventory
// store, mirroring MarketScanner's scan-and-save shape. On the FIRST heavy-type
// discovery of the era it emits one milestone captain event — the signal the
// fleet autosizer's fail-closed heavy branch has been waiting on.
//
// IT IS ALSO THE FLEET'S ONE SHIPYARD-READ CHOKE POINT. Every path in
// the daemon that costs a GET /shipyard request now reaches ReadShipyard below, so
// the server's unraisable 2.00 req/s ceiling is defended in exactly one place.
// That was not true when this type was written: four call sites reached
// APIClient.GetShipyard directly, which is how shipyard reads measured 0.844 req/s
// — 44.7% of the whole ceiling — while this scanner honoured its own recency
// window. Those four now come through here.
type ShipyardScanner struct {
	apiClient     shipyardAPI
	inventoryRepo shipyard.InventoryRepository
	waypointRepo  waypointTraitReader
	events        captain.EventRecorder
	heavyTypes    shipyard.HeavyShipTypeSet
	rescanTTL     time.Duration

	// budget is the fleet's ONE shipyard-read allowance, and it is never nil —
	// see NewShipyardScanner.
	budget *YardScanBudget
}

// DefaultShipyardRescanTTL is the recency window between live reads of one
// shipyard. It is deliberately kept in the same order of magnitude as the
// re-scan cadence the fleet already runs, because these rows are not
// discovery-only: the reachable-yard ranking and the fleet autosizer's
// heavy-price signal judge probe and hull buys on this stored PurchasePrice, so
// the window doubles as the staleness bound on a money-guard input. Widening it
// to hours would age that input by an order of magnitude and stretch the gap
// that the per-yard recent-buy impact term already exists to cover.
const DefaultShipyardRescanTTL = 15 * time.Minute

// NewShipyardScanner creates the scanner. events may be nil (milestone becomes
// log-only); heavyTypes built from config (empty config → default set).
// rescanTTL of 0 or less resolves to DefaultShipyardRescanTTL — the recency
// window is always active, config tunes its size but never removes it.
func NewShipyardScanner(
	apiClient shipyardAPI,
	inventoryRepo shipyard.InventoryRepository,
	waypointRepo waypointTraitReader,
	events captain.EventRecorder,
	heavyTypes shipyard.HeavyShipTypeSet,
	rescanTTL time.Duration,
) *ShipyardScanner {
	if rescanTTL <= 0 {
		rescanTTL = DefaultShipyardRescanTTL
	}
	s := &ShipyardScanner{
		apiClient:     apiClient,
		inventoryRepo: inventoryRepo,
		waypointRepo:  waypointRepo,
		events:        events,
		heavyTypes:    heavyTypes,
		rescanTTL:     rescanTTL,
		budget:        NewYardScanBudget(defaultYardBudgetReqPerSec, defaultYardValueClampR, heavyTypes),
	}
	// The budget is constructed here rather than injected, and that is deliberate:
	// it means there is no way to build a scanner that does not enforce it. A
	// composition root with configured knobs replaces the default via
	// SetScanBudget; a caller that forgets still gets a paced scanner, so the
	// failure mode of forgetting is "paced at the default rate", never "unmetered".
	if inventoryRepo != nil {
		s.budget.SetYardCatalogReader(inventoryRepo)
	}
	return s
}

// SetScanBudget replaces the default shipyard-read allowance with a configured
// one. A nil budget is ignored: there is no supported way to run this scanner
// unpaced.
func (s *ShipyardScanner) SetScanBudget(b *YardScanBudget) {
	if b == nil {
		return
	}
	if s.inventoryRepo != nil {
		b.SetYardCatalogReader(s.inventoryRepo)
	}
	s.budget = b
}

// ScanBudget exposes the allowance for metrics and the operator report.
func (s *ShipyardScanner) ScanBudget() *YardScanBudget { return s.budget }

// ScanAndSaveShipyard scans the shipyard at waypointSymbol (if the waypoint
// bears the SHIPYARD trait) and persists availability + prices. Non-shipyard
// waypoints are a silent no-op — this is called on EVERY scout market visit,
// and the trait check is a cached-waypoint read, so the no-op path spends no
// API budget. A yard whose rows are newer than the rescan window is skipped
// without a live read; a yard never scanned this era is always read. Errors are
// returned for the caller to log; a scan failure must never fail the tour that
// hosts it.
func (s *ShipyardScanner) ScanAndSaveShipyard(ctx context.Context, playerID uint, waypointSymbol string) error {
	_, err := s.ReadShipyard(ctx, playerID, waypointSymbol, marketscan.Discretionary)
	return err
}

// ReadShipyard is the fleet's ONE metered shipyard read: it admits the request
// against the shipyard-read budget, performs the live GET when admitted, persists
// what it found, and hands the live data back to the caller.
//
// THE RETURN CONTRACT IS THE WHOLE POINT, so it is spelled out:
//
//   - (data, nil) — the read happened; data is live and has been persisted.
//   - (nil, nil)  — DECLINED. Not an error: the budget served this yard from
//     store, and the caller's next act is to read the persisted rows, which is
//     what it would have done after a scan anyway. Returning an error here would
//     instead trip the fail-closed money guards downstream.
//   - (nil, err)  — the read was attempted and failed (token, or the API).
//   - (data, err) — the read succeeded but persistence did not. The data is still
//     live and still correct, and a caller that needs a price must use it: a
//     store hiccup must never be allowed to fail a money guard's live read.
//
// CLASS DECIDES WHAT MAY BE SKIPPED. A Discretionary read is filtered first by the
// cached SHIPYARD trait (a non-shipyard is a free no-op, no API budget), then by
// the rescan window, then by the budget. An Earning read — one whose result a
// fail-closed money guard consumes before committing a purchase — skips both
// filters and is never declined. It is still METERED: it draws its token from the
// same allowance, so pre-buy verification squeezes discretionary scanning instead
// of being added on top of it, which is what keeps this one number the honest
// total (RULINGS #4: money guards are never weakened).
//
// The trait filter in particular MUST NOT apply to an Earning read. That check
// reads a local cache and answers "not a shipyard" for any waypoint the cache has
// not warmed — so applying it to a money guard would let a cold cache silently
// answer a pre-buy price check with "no shipyard here", which is a stale-store
// answer wearing an error's clothes. The caller of an Earning read has already
// established it is standing at a yard.
func (s *ShipyardScanner) ReadShipyard(ctx context.Context, playerID uint, waypointSymbol string, class marketscan.Class) (*domainPorts.ShipyardData, error) {
	logger := common.LoggerFromContext(ctx)
	deniable := class != marketscan.Earning

	if deniable && !s.isShipyardWaypoint(ctx, waypointSymbol) {
		return nil, nil
	}

	lastScanned, known := s.lastScan(ctx, int(playerID), waypointSymbol)

	// The rescan window is a FLOOR the budget may not scan past, never the thing
	// that sets the rate — the budget widens intervals on its own as the map grows,
	// and this only stops a burst of arrivals re-reading one yard within minutes.
	if deniable && known && !lastScanned.IsZero() && time.Since(lastScanned) < s.rescanTTL {
		logger.Log("INFO", fmt.Sprintf("[ShipyardScanner] Skipping scan of %s - inventory scanned within %s", waypointSymbol, s.rescanTTL), map[string]interface{}{
			"action": "shipyard_scan_skipped_fresh", "waypoint": waypointSymbol, "ttl_seconds": int(s.rescanTTL.Seconds()),
		})
		return nil, nil
	}

	if s.budget.Admit(ctx, int(playerID), waypointSymbol, lastScanned, known, class) == marketscan.ServeFromStore {
		logger.Log("DEBUG", fmt.Sprintf("[ShipyardScanner] Serving %s from store - shipyard-read budget", waypointSymbol), map[string]interface{}{
			"action": "shipyard_scan_served_from_store", "waypoint": waypointSymbol,
		})
		return nil, nil
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get player token: %w", err)
	}

	systemSymbol := shared.ExtractSystemSymbol(waypointSymbol)
	data, err := s.apiClient.GetShipyard(ctx, systemSymbol, waypointSymbol, token)
	if err != nil {
		return nil, fmt.Errorf("failed to get shipyard data for %s: %w", waypointSymbol, err)
	}
	if data == nil {
		// A nil payload with no error is a broken client, not an empty yard.
		// Flattening it would panic; reporting it lets the fail-closed guards do
		// their job.
		return nil, fmt.Errorf("shipyard read for %s returned no data", waypointSymbol)
	}

	scannedAt := time.Now()
	availabilities := availabilitiesFromScan(systemSymbol, waypointSymbol, data, scannedAt)

	// Fold what this yard turned out to sell into its value weight, so the next
	// rotation ranks it on its real catalogue rather than on the optimistic prior
	// it carried while unseen.
	s.budget.Observe(waypointSymbol, availabilities)

	// A reading whose price and supply disagree says the API no longer pairs them, which is the
	// evidence the buy path's presence gate rests on. Refused rather than cached; data still returns.
	if offenders := shipyard.DisagreeingRows(availabilities); len(offenders) > 0 {
		logger.Log("ERROR", fmt.Sprintf("[ShipyardScanner] %s returned %d listing(s) whose price and supply disagree; refusing to cache the reading", waypointSymbol, len(offenders)), map[string]interface{}{
			"action": "shipyard_scan_discriminator_violation", "waypoint": waypointSymbol, "rows": len(offenders),
		})
		return data, fmt.Errorf("shipyard reading for %s breaks the price/supply discriminator on %d row(s)", waypointSymbol, len(offenders))
	}

	// The milestone predicate must be read BEFORE the scan is persisted, or the
	// freshly written rows would make every first discovery look already-known.
	firstHeavy, heavyFound := s.isFirstHeavyDiscovery(ctx, int(playerID), availabilities)

	if err := s.inventoryRepo.ReplaceScan(ctx, int(playerID), systemSymbol, waypointSymbol, availabilities, scannedAt); err != nil {
		return data, fmt.Errorf("failed to persist shipyard inventory for %s: %w", waypointSymbol, err)
	}

	logger.Log("INFO", fmt.Sprintf("[ShipyardScanner] Scanned shipyard %s (%d ship types)", waypointSymbol, len(availabilities)), map[string]interface{}{
		"action":   "scan_shipyard",
		"waypoint": waypointSymbol,
		"types":    len(availabilities),
	})

	if firstHeavy {
		s.emitHeavyYardMilestone(ctx, int(playerID), systemSymbol, waypointSymbol, heavyFound, logger)
	}
	return data, nil
}

// OffersFor returns every persisted yard row selling shipType — the store answer a
// caller falls back to when the budget declines its live read.
//
// One query answers a whole system's worth of candidates, which is what lets the
// two catalogue-searching callers stop issuing a live read PER YARD. A row with
// PurchasePrice 0 is catalogued but never priced: proof the yard sells the type,
// never a price to spend against.
func (s *ShipyardScanner) OffersFor(ctx context.Context, playerID int, shipType string) ([]shipyard.ShipTypeAvailability, error) {
	if s.inventoryRepo == nil || shipType == "" {
		return nil, nil
	}
	return s.inventoryRepo.ListByTypes(ctx, playerID, []string{shipType})
}

// NoteDemand tells the budget the fleet is shopping for this hull type, so every
// yard known to sell it rises in the rotation. Callers that search a catalogue
// name their type here; the budget needs no other demand feed.
func (s *ShipyardScanner) NoteDemand(shipType string) { s.budget.NoteDemand(shipType) }

// NoteTarget tells the budget a money guard priced a hull at this yard, so the
// rotation keeps that counter fresh while the fleet is buying there.
func (s *ShipyardScanner) NoteTarget(waypoint string) { s.budget.NoteTarget(waypoint) }

// lastScan reads this yard's persisted scan stamp. Every uncertainty resolves to
// NOT KNOWN: a yard with no row this era, an unreadable stamp, or a store error
// must never grant a skip — skipping is the optimization, so an unproven "we
// already have this" is not one. The flags are honored BEFORE the timestamp is
// used, so a store that returns a stale-but-plausible value alongside a failure
// signal cannot suppress a required scan. Not-known also reads as infinitely stale
// to the budget, so it is admitted rather than declined into darkness.
func (s *ShipyardScanner) lastScan(ctx context.Context, playerID int, waypointSymbol string) (time.Time, bool) {
	lastScanned, known, err := s.inventoryRepo.LastScannedAt(ctx, playerID, waypointSymbol)
	if err != nil || !known || lastScanned.IsZero() {
		return time.Time{}, false
	}
	return lastScanned, true
}

// isShipyardWaypoint reports whether the cached waypoint bears the SHIPYARD
// trait, read as an immutable fact (era-agnostic, TTL-agnostic — see
// waypointTraitReader). An uncached waypoint or a read error reads as "not a
// shipyard" — the scan simply retries on the next visit once the cache is warm;
// never an API probe.
func (s *ShipyardScanner) isShipyardWaypoint(ctx context.Context, waypointSymbol string) bool {
	if s.waypointRepo == nil {
		return false
	}
	hasShipyard, err := s.waypointRepo.HasWaypointTrait(ctx, waypointSymbol, "SHIPYARD")
	if err != nil {
		return false
	}
	return hasShipyard
}

// availabilitiesFromScan flattens a live shipyard read into one row per listed
// ship type: the union of the availability list (shipTypes) and the priced
// listings (ships). A type with a priced listing carries its price + supply; a
// type only in the availability list persists with price 0 (availability known,
// unpriced — it can prove discovery but never feed a price guard).
func availabilitiesFromScan(systemSymbol, waypointSymbol string, data *domainPorts.ShipyardData, scannedAt time.Time) []shipyard.ShipTypeAvailability {
	byType := make(map[string]shipyard.ShipTypeAvailability)
	order := make([]string, 0, len(data.ShipTypes)+len(data.Ships))
	add := func(shipType string, price int, supply string) {
		if shipType == "" {
			return
		}
		if _, seen := byType[shipType]; !seen {
			order = append(order, shipType)
		}
		existing := byType[shipType]
		if existing.PurchasePrice == 0 || price > 0 {
			byType[shipType] = shipyard.ShipTypeAvailability{
				SystemSymbol:   systemSymbol,
				WaypointSymbol: waypointSymbol,
				ShipType:       shipType,
				PurchasePrice:  max(price, existing.PurchasePrice),
				Supply:         firstNonEmpty(supply, existing.Supply),
				LastScanned:    scannedAt,
			}
		}
	}
	for _, st := range data.ShipTypes {
		add(st.Type, 0, "")
	}
	for _, listing := range data.Ships {
		add(listing.Type, listing.PurchasePrice, listing.Supply)
	}
	out := make([]shipyard.ShipTypeAvailability, 0, len(order))
	for _, shipType := range order {
		out = append(out, byType[shipType])
	}
	return out
}

// isFirstHeavyDiscovery reports whether this scan is the era's FIRST heavy-type
// discovery for the player, and which heavy rows it found. A store read failure
// suppresses the milestone (never the scan): a duplicate-suppression predicate
// that cannot be read must not risk duplicate news.
func (s *ShipyardScanner) isFirstHeavyDiscovery(ctx context.Context, playerID int, availabilities []shipyard.ShipTypeAvailability) (bool, []shipyard.ShipTypeAvailability) {
	heavyFound := make([]shipyard.ShipTypeAvailability, 0, 2)
	for _, a := range availabilities {
		if s.heavyTypes.Contains(a.ShipType) {
			heavyFound = append(heavyFound, a)
		}
	}
	if len(heavyFound) == 0 {
		return false, nil
	}
	alreadyKnown, err := s.inventoryRepo.HasAnyOfTypes(ctx, playerID, s.heavyTypes.Members())
	if err != nil || alreadyKnown {
		return false, nil
	}
	return true, heavyFound
}

// emitHeavyYardMilestone records the one-time-per-era heavy-yard discovery
// (log + captain event). Event failures are logged, never returned — the scan
// already persisted; losing the notice must not fail the visit.
func (s *ShipyardScanner) emitHeavyYardMilestone(ctx context.Context, playerID int, systemSymbol, waypointSymbol string, heavyFound []shipyard.ShipTypeAvailability, logger common.ContainerLogger) {
	types := make([]string, 0, len(heavyFound))
	prices := map[string]int{}
	for _, a := range heavyFound {
		types = append(types, a.ShipType)
		prices[a.ShipType] = a.PurchasePrice
	}
	logger.Log("INFO", fmt.Sprintf("[ShipyardScanner] MILESTONE: first heavy-freight yard discovered this era at %s (%v)", waypointSymbol, types), map[string]interface{}{
		"action":   "heavy_yard_discovered",
		"system":   systemSymbol,
		"waypoint": waypointSymbol,
		"types":    types,
	})
	if s.events == nil {
		return
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"system": systemSymbol, "waypoint": waypointSymbol, "types": types, "prices": prices,
	})
	if err := s.events.Record(ctx, &captain.Event{
		Type:     captain.EventHeavyYardDiscovered,
		PlayerID: playerID,
		Payload:  string(payload),
	}); err != nil {
		logger.Log("WARN", fmt.Sprintf("[ShipyardScanner] heavy-yard milestone event failed to record: %v", err), nil)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
