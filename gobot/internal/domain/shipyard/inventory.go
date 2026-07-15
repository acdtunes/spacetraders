package shipyard

import (
	"context"
	"sort"
	"time"
)

// ShipTypeAvailability is one scanned shipyard fact (sp-42ow): at scan time,
// this (system, waypoint) shipyard offered ship_type, at purchase_price, with
// the listing's supply tier. It is a CQRS read-model row (quality-framework
// Rule 9 relaxation): the scanner writes it, the reachable-yard ranking and the
// fleet autosizer's yard-price signal read it. PurchasePrice 0 means the type
// was LISTED (availability known) but carried no priced listing at scan time —
// such rows still prove availability but can never feed a price guard.
type ShipTypeAvailability struct {
	SystemSymbol   string
	WaypointSymbol string
	ShipType       string
	PurchasePrice  int
	Supply         string
	LastScanned    time.Time
}

// DefaultHeavyShipTypes is the default heavy-freight hull set (sp-42ow): the
// classes whose first discovered yard is fleet-strategy news (the autosizer's
// heavy branch fails closed until one is known and reachable). Overridable via
// [scouting] heavy_ship_types in config.yaml (RULINGS #5).
var DefaultHeavyShipTypes = []string{"SHIP_HEAVY_FREIGHTER", "SHIP_BULK_FREIGHTER"}

// HeavyShipTypeSet is the configured set of ship types that count as heavy
// freight for yard discovery. An empty configuration resolves to
// DefaultHeavyShipTypes, so the milestone detection never runs with a silently
// empty set.
type HeavyShipTypeSet struct {
	members map[string]struct{}
}

// NewHeavyShipTypeSet builds the set from configured types; nil/empty defers
// to DefaultHeavyShipTypes.
func NewHeavyShipTypeSet(types []string) HeavyShipTypeSet {
	if len(types) == 0 {
		types = DefaultHeavyShipTypes
	}
	members := make(map[string]struct{}, len(types))
	for _, t := range types {
		if t == "" {
			continue
		}
		members[t] = struct{}{}
	}
	return HeavyShipTypeSet{members: members}
}

// Contains reports whether shipType counts as heavy under this configuration.
func (s HeavyShipTypeSet) Contains(shipType string) bool {
	_, ok := s.members[shipType]
	return ok
}

// Members returns the configured types, sorted for deterministic queries/logs.
func (s HeavyShipTypeSet) Members() []string {
	out := make([]string, 0, len(s.members))
	for t := range s.members {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// InventoryRepository is the driven port for the persisted shipyard-inventory
// store (sp-42ow) — the shipyard twin of the market_data cache. One row per
// (player, waypoint, ship_type); a re-scan REPLACES the waypoint's row set
// (upsert semantics: prices/last_scanned refresh, delisted types disappear,
// never a duplicate row). Reads are era-scoped by the implementation so a
// universe reset never leaks dead-era yards into live signals.
type InventoryRepository interface {
	// ReplaceScan atomically swaps the (player, waypoint) row set for the
	// fresh scan result, stamping the open era and scannedAt.
	ReplaceScan(ctx context.Context, playerID int, systemSymbol, waypointSymbol string, availabilities []ShipTypeAvailability, scannedAt time.Time) error
	// HasAnyOfTypes reports whether ANY era-scoped row for the player carries
	// one of shipTypes — the "have we ever discovered a heavy yard this era"
	// predicate behind the one-time milestone.
	HasAnyOfTypes(ctx context.Context, playerID int, shipTypes []string) (bool, error)
	// ListByTypes returns every era-scoped row for the player whose ship_type
	// is in shipTypes, for the reachable-yard ranking.
	ListByTypes(ctx context.Context, playerID int, shipTypes []string) ([]ShipTypeAvailability, error)
}
