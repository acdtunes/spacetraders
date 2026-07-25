package ship

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
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
type ShipyardScanner struct {
	apiClient     shipyardAPI
	inventoryRepo shipyard.InventoryRepository
	waypointRepo  waypointTraitReader
	events        captain.EventRecorder
	heavyTypes    shipyard.HeavyShipTypeSet
	rescanTTL     time.Duration
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
	return &ShipyardScanner{
		apiClient:     apiClient,
		inventoryRepo: inventoryRepo,
		waypointRepo:  waypointRepo,
		events:        events,
		heavyTypes:    heavyTypes,
		rescanTTL:     rescanTTL,
	}
}

// ScanAndSaveShipyard scans the shipyard at waypointSymbol (if the waypoint
// bears the SHIPYARD trait) and persists availability + prices. Non-shipyard
// waypoints are a silent no-op — this is called on EVERY scout market visit,
// and the trait check is a cached-waypoint read, so the no-op path spends no
// API budget. A yard whose rows are newer than the rescan window is skipped
// without a live read; a yard never scanned this era is always read. Errors are
// returned for the caller to log; a scan failure must never fail the tour that
// hosts it.
func (s *ShipyardScanner) ScanAndSaveShipyard(ctx context.Context, playerID uint, waypointSymbol string) error {
	if !s.isShipyardWaypoint(ctx, waypointSymbol) {
		return nil
	}
	logger := common.LoggerFromContext(ctx)

	if s.scannedWithinWindow(ctx, int(playerID), waypointSymbol) {
		logger.Log("INFO", fmt.Sprintf("[ShipyardScanner] Skipping scan of %s - inventory scanned within %s", waypointSymbol, s.rescanTTL), map[string]interface{}{
			"action": "shipyard_scan_skipped_fresh", "waypoint": waypointSymbol, "ttl_seconds": int(s.rescanTTL.Seconds()),
		})
		return nil
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get player token: %w", err)
	}

	systemSymbol := shared.ExtractSystemSymbol(waypointSymbol)
	data, err := s.apiClient.GetShipyard(ctx, systemSymbol, waypointSymbol, token)
	if err != nil {
		return fmt.Errorf("failed to get shipyard data for %s: %w", waypointSymbol, err)
	}

	scannedAt := time.Now()
	availabilities := availabilitiesFromScan(systemSymbol, waypointSymbol, data, scannedAt)

	// The milestone predicate must be read BEFORE the scan is persisted, or the
	// freshly written rows would make every first discovery look already-known.
	firstHeavy, heavyFound := s.isFirstHeavyDiscovery(ctx, int(playerID), availabilities)

	if err := s.inventoryRepo.ReplaceScan(ctx, int(playerID), systemSymbol, waypointSymbol, availabilities, scannedAt); err != nil {
		return fmt.Errorf("failed to persist shipyard inventory for %s: %w", waypointSymbol, err)
	}

	logger.Log("INFO", fmt.Sprintf("[ShipyardScanner] Scanned shipyard %s (%d ship types)", waypointSymbol, len(availabilities)), map[string]interface{}{
		"action":   "scan_shipyard",
		"waypoint": waypointSymbol,
		"types":    len(availabilities),
	})

	if firstHeavy {
		s.emitHeavyYardMilestone(ctx, int(playerID), systemSymbol, waypointSymbol, heavyFound, logger)
	}
	return nil
}

// scannedWithinWindow reports whether this yard already holds rows newer than
// the rescan window, so the live read can be skipped. Every uncertainty resolves
// to FALSE (scan): a yard with no row this era, an unreadable stamp, or a store
// error must never grant a skip — skipping is the optimization, so an unproven
// "we already have this" is not one. The flags are honored BEFORE the timestamp
// is used, so a store that returns a stale-but-plausible value alongside a
// failure signal cannot suppress a required scan. A skip writes nothing, leaving
// last_scanned pointing at the real observation.
func (s *ShipyardScanner) scannedWithinWindow(ctx context.Context, playerID int, waypointSymbol string) bool {
	lastScanned, known, err := s.inventoryRepo.LastScannedAt(ctx, playerID, waypointSymbol)
	if err != nil || !known || lastScanned.IsZero() {
		return false
	}
	return time.Since(lastScanned) < s.rescanTTL
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
