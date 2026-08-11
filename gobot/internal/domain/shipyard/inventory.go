package shipyard

import (
	"context"
	"sort"
	"strings"
	"time"
)

// ShipTypeAvailability is one scanned shipyard fact: at scan time,
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

// PriceSupplyDisagree reports a row whose price and supply tell different stories about how it was
// read. A shipyard quotes an ask and its supply TOGETHER under presence, and neither without one, so
// the pair is the store's record of read mode — and the buy path's presence gate rests on that record.
// The SQL half of this check lives on the repository, so a writer that bypasses one meets the other.
func PriceSupplyDisagree(a ShipTypeAvailability) bool {
	return (a.PurchasePrice > 0) != (strings.TrimSpace(a.Supply) != "")
}

// DisagreeingRows is PriceSupplyDisagree over a whole reading, naming every offender so a refusal can
// say which row broke the rule rather than only that one did.
func DisagreeingRows(rows []ShipTypeAvailability) []ShipTypeAvailability {
	var out []ShipTypeAvailability
	for _, a := range rows {
		if PriceSupplyDisagree(a) {
			out = append(out, a)
		}
	}
	return out
}

// heavyHullClasses is the SINGLE source of truth for what "heavy" means, pairing
// each heavy shipyard ship type with the frame symbol that same hull reports once
// it is owned. The two identifiers name the same hull class in two different
// tables — shipyard_inventory keys on ship_type, the ships table carries only
// frame_symbol (there is no ship_type column on ships and no ship-type→frame
// mapping anywhere else in the tree) — so a heavy census over owned hulls and a
// heavy-yard query over inventory MUST agree on the pairing or they silently
// disagree about the same fleet.
//
// ONE table, two projections (DefaultHeavyShipTypes / DefaultHeavyFrameSymbols),
// the RULINGS #5 "one base, derived tiers" idiom the reserve floors use: adding a
// heavy class means adding one row here, and it is not possible to add it to only
// one side. TestHeavyHullClassPairing pins that neither projection is empty and
// that every row carries both halves, so a half-filled row fails the suite LOUD
// rather than under-counting owned heavies — the direction that would authorise
// buying a hull we already own.
var heavyHullClasses = []struct {
	ShipType    string
	FrameSymbol string
}{
	{ShipType: "SHIP_HEAVY_FREIGHTER", FrameSymbol: "FRAME_HEAVY_FREIGHTER"},
	{ShipType: "SHIP_BULK_FREIGHTER", FrameSymbol: "FRAME_BULK_FREIGHTER"},
}

// DefaultHeavyShipTypes is the default heavy-freight hull set: the
// classes whose first discovered yard is fleet-strategy news (the autosizer's
// heavy branch fails closed until one is known and reachable). Overridable via
// [scouting] heavy_ship_types in config.yaml (RULINGS #5). Projected from
// heavyHullClasses so it can never drift from the frame set.
var DefaultHeavyShipTypes = func() []string {
	out := make([]string, 0, len(heavyHullClasses))
	for _, c := range heavyHullClasses {
		out = append(out, c.ShipType)
	}
	return out
}()

// DefaultHeavyFrameSymbols is the owned-hull projection of the same table: the
// frame symbols an owned heavy reports. This is what an owned-heavy census keys
// on, because the ships table stores frame_symbol and not ship_type.
var DefaultHeavyFrameSymbols = func() []string {
	out := make([]string, 0, len(heavyHullClasses))
	for _, c := range heavyHullClasses {
		out = append(out, c.FrameSymbol)
	}
	return out
}()

// lightFreightClass is the freight hull class BELOW the heavy classes: the one the
// fleet actually owns and flies its trade lanes with today.
//
// Unlike the heavy pairings above, this one is OBSERVED rather than inferred — the
// live trade pool is FRAME_LIGHT_FREIGHTER at 80 cargo, bought as SHIP_LIGHT_HAULER
// (verified 2026-07-28: 10 trade-tagged + 3 contract-tagged hulls, all 80 cargo).
// It is deliberately NOT added to heavyHullClasses: 80 cargo is far below
// HeavyCargoCapacityThreshold and calling it heavy would both inflate the owned-heavy
// census and close the heavy capability against hulls that are not heavies.
var lightFreightClass = struct{ ShipType, FrameSymbol string }{
	ShipType: "SHIP_LIGHT_HAULER", FrameSymbol: "FRAME_LIGHT_FREIGHTER",
}

// TradeHullPreferenceOrder is every hull class that can fly a TRADE LANE, BEST FIRST
// — the heavy freight classes, then the light freighter.
//
// ORDERED BY FREIGHT CAPABILITY, NOT BY NAME. A trade lane's value scales with the
// cargo carried per round trip, so the largest freight hull that can actually be
// priced is the right substitute when the preferred one cannot be. The heavy classes
// are projected from heavyHullClasses — the same single source of truth the census
// and the yard query use — so this list cannot drift from them, and a newly-added
// heavy class automatically outranks the light tier here too.
//
// SELF-CORRECTING BY CONSTRUCTION: the heavies sort FIRST, so a consumer that walks
// this order and takes the first priceable type returns to the preferred hull the
// moment a heavy yard is discovered, with no intervention and no config change.
//
// WHAT IS DELIBERATELY EXCLUDED, and why it is not an oversight. SHIP_LIGHT_SHUTTLE,
// SHIP_PROBE, SHIP_MINING_DRONE, SHIP_SIPHON_DRONE, SHIP_SURVEYOR and SHIP_EXPLORER
// are all priceable at yards we have discovered, and none belongs here: a shuttle's
// hold is a courier's rather than a trader's, and the rest carry no useful freight at
// all. The exclusion matters because the autosizer's realized-rate guards judge the
// FLEET's rate, not the candidate hull's capability — they cannot tell that a
// substituted hull is too small to earn, so an undersized substitute would be bought
// and only discovered later. Keeping the list to genuine freight hulls is what makes
// the substitution safe; when nothing here can be priced the caller buys nothing,
// which is the correct fail-closed outcome.
var TradeHullPreferenceOrder = func() []string {
	out := make([]string, 0, len(heavyHullClasses)+1)
	for _, c := range heavyHullClasses {
		out = append(out, c.ShipType)
	}
	return append(out, lightFreightClass.ShipType)
}()

// HeavyCargoCapacityThreshold is the cargo hold at or above which a hull counts as
// heavy REGARDLESS of its frame — the safety net under the frame list.
//
// Why a net is needed: the frame symbols above are INFERRED from SpaceTraders'
// SHIP_/FRAME_ naming symmetry, not observed. The fleet owns no heavy hull to read
// one from (verified 2026-07-27: FRAME_LIGHT_FREIGHTER ×8 at 80 cargo, FRAME_FRIGATE
// ×1 at 40, FRAME_PROBE ×3 at 0), and a wrong symbol would UNDER-count — the
// money-unsafe direction, since an invisible heavy leaves the reservation open and
// authorises re-buying a hull we already own.
//
// Why 120: it sits in the wide, empirically-verified gap between the largest hull the
// fleet owns (80) and a heavy freighter's hold (~225). Both bounds matter and are
// pinned by test. Too LOW and ordinary fleet growth would classify working haulers as
// heavy, close the capability and stall heavy buying entirely; too HIGH and a real
// heavy slips under the net, which is exactly the under-count the net exists to stop.
// Sitting mid-gap is the most defensible position against both errors.
//
// The net deliberately OVER-counts in the ambiguous case (an unrecognised large hull
// counts as heavy), which buys FEWER heavies — the safe direction.
const HeavyCargoCapacityThreshold = 120

// IsHeavyHull classifies one owned hull for the heavy census. The frame list is
// PRIMARY and authoritative: a known-heavy frame counts whatever its cargo capacity,
// so a heavy that reports an unexpected hold is never missed.
//
// unrecognisedFrame reports the safety net FIRING: the hull is large enough to be a
// heavy but its frame is not in the known list. The caller MUST log that loudly — it
// is the only signal available that the inferred frame list is incomplete, because no
// owned heavy exists to check it against. The first such line in production is the
// list asking to be corrected; the frame list stays authoritative and gets amended
// from it. A hull matched by frame is NOT flagged (nothing was inferred).
func IsHeavyHull(frameSymbol string, cargoCapacity int) (heavy bool, unrecognisedFrame bool) {
	for _, c := range heavyHullClasses {
		if c.FrameSymbol == frameSymbol {
			return true, false
		}
	}
	if cargoCapacity >= HeavyCargoCapacityThreshold {
		return true, true
	}
	return false, false
}

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
// store — the shipyard twin of the market_data cache. One row per
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
	// LastScannedAt returns when this waypoint's rows were last stamped and
	// whether any era-scoped row exists at all. known=false means the yard has
	// never been scanned this era, which callers must treat as "scan now" —
	// discovery is never delayed by a recency window.
	LastScannedAt(ctx context.Context, playerID int, waypointSymbol string) (scannedAt time.Time, known bool, err error)
}
