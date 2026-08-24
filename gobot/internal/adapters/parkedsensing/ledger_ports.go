package parkedsensing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// ---- LedgerPort -------------------------------------------------------------

// LedgerPort translates the durable sensing ledger's rows into the engine's own
// view of a placement, and its transitions back into row writes.
//
// The translation exists because the engine must not see persistence types: it
// flattens the ledger's nullable ship and yard columns to plain strings, since
// nothing in the engine distinguishes NULL from empty and every check it makes
// is "is a hull recorded here?".
type LedgerPort struct {
	repo *persistence.SensingLedgerRepository
}

// NewLedgerPort wires the placement ledger.
func NewLedgerPort(repo *persistence.SensingLedgerRepository) *LedgerPort {
	return &LedgerPort{repo: repo}
}

// SlotsByState returns the player's placements in any of the given states.
func (p *LedgerPort) SlotsByState(ctx context.Context, playerID int, states ...string) ([]appSensing.QueuedSlot, error) {
	models, err := p.repo.SlotsByState(ctx, playerID, states...)
	if err != nil {
		return nil, err
	}
	return queuedSlots(models), nil
}

// PlacementWorklist returns the placements in any of the given states, least
// recently attempted first. Deliberately a different read from SlotsByState
// rather than a re-sort of it: the ORDER is the contract the placement machine
// depends on, and it is the opposite of the one every other caller wants. See the
// repository method for why a tick-stable order starves the list.
func (p *LedgerPort) PlacementWorklist(ctx context.Context, playerID int, states ...string) ([]appSensing.QueuedSlot, error) {
	models, err := p.repo.PlacementWorklist(ctx, playerID, states...)
	if err != nil {
		return nil, err
	}
	return queuedSlots(models), nil
}

// MarkPlacementAttempt stamps a slot as having just consumed a placement tick's
// budget, sending it to the back of the worklist.
func (p *LedgerPort) MarkPlacementAttempt(ctx context.Context, playerID int, waypoint, kind string) error {
	return p.repo.MarkPlacementAttempt(ctx, playerID, waypoint, kind)
}

// SlotsBySystem returns every placement in one system, in any state.
func (p *LedgerPort) SlotsBySystem(ctx context.Context, playerID int, system string) ([]appSensing.QueuedSlot, error) {
	models, err := p.repo.SlotsBySystem(ctx, playerID, system)
	if err != nil {
		return nil, err
	}
	return queuedSlots(models), nil
}

// ExistingSlots returns the placements a system already holds, carrying the
// whitelisted goods and depth recorded when each was written.
//
// DECODING whitelist_goods is the whole point of this method, not a detail of
// it. The screen uses the recorded goods as a CACHE: a waypoint discovered
// remotely has no market_data rows until a probe actually parks there, so
// without the recorded list the screen would pay the API to re-fetch the same
// answer on every re-screen of the system. Worse, it must SUPPLY the goods
// rather than merely suppress the fetch — an empty list drops the waypoint out
// of the hit set and takes its own slot back out of the plan, so a remotely
// discovered market would be re-found and re-dropped forever. A decode failure
// is therefore an error, never a silent empty list.
func (p *LedgerPort) ExistingSlots(ctx context.Context, playerID int, system string) ([]appSensing.ExistingSlot, error) {
	models, err := p.repo.SlotsBySystem(ctx, playerID, system)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.ExistingSlot, 0, len(models))
	for _, m := range models {
		goods, derr := decodeWhitelistGoods(m.WhitelistGoods)
		if derr != nil {
			return nil, fmt.Errorf("failed to decode the recorded goods at %q: %w", m.WaypointSymbol, derr)
		}
		out = append(out, appSensing.ExistingSlot{
			Waypoint:       m.WaypointSymbol,
			WhitelistGoods: goods,
			DepthCredits:   m.DepthCredits,
		})
	}
	return out, nil
}

// ParkedSlotViews returns every PARKED placement as the scan rotation sees it.
//
// It is a separate read from SlotsByState because QueuedSlot is a STATE
// projection and the rotation paces on three columns it does not carry: the
// whitelist the slot exists to watch, the smoothed spread that weights it, and
// the last scan stamp that makes it due. YardCadence is deliberately left zero —
// it is a knob, and the coordinator stamps it, because an adapter that invented
// a cadence would be making an operator's decision from the persistence layer.
func (p *LedgerPort) ParkedSlotViews(ctx context.Context, playerID int) ([]appSensing.SensingSlotView, error) {
	models, err := p.repo.SlotsByState(ctx, playerID, appSensing.SlotStateParked)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.SensingSlotView, 0, len(models))
	for _, m := range models {
		goods, derr := decodeWhitelistGoods(m.WhitelistGoods)
		if derr != nil {
			return nil, fmt.Errorf("failed to decode the watched goods at %q: %w", m.WaypointSymbol, derr)
		}
		view := appSensing.SensingSlotView{
			Waypoint:   m.WaypointSymbol,
			Kind:       m.SlotKind,
			State:      m.State,
			Whitelist:  goods,
			SpreadEWMA: m.SpreadEWMA,
		}
		// The rotation paces on the ATTEMPT clock and the staleness gauge reads the
		// DATA clock, because a budget decline advances the first and not the second.
		// Reading one column into both collapses the two.
		//
		// THE COALESCE IS THE MIGRATION. AutoMigrate adds last_scan_attempt_at as
		// NULL on every existing row, and a NULL read as the zero time would declare
		// the entire rotation due on the first tick after deploy — one full-speed
		// sweep of every slot. Falling back to last_scan_at makes that first tick
		// pace as the last one did; the attempt clock takes over from the first turn
		// each slot then takes.
		if m.LastScanAttemptAt != nil {
			view.LastScan = *m.LastScanAttemptAt
		} else if m.LastScanAt != nil {
			view.LastScan = *m.LastScanAt
		}
		if m.LastScanAt != nil {
			view.LastDataAt = *m.LastScanAt
		}
		out = append(out, view)
	}
	return out, nil
}

// decodeWhitelistGoods reads the JSON goods column. An empty column is an empty
// list rather than an error: rows written before the column had a default, and
// YARD slots, both legitimately carry nothing.
func decodeWhitelistGoods(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var goods []string
	if err := json.Unmarshal([]byte(raw), &goods); err != nil {
		return nil, err
	}
	return goods, nil
}

// SystemsByVerdict returns the systems carrying a screening verdict.
func (p *LedgerPort) SystemsByVerdict(ctx context.Context, playerID int, verdict string) ([]appSensing.ScreenedSystem, error) {
	models, err := p.repo.SystemsByVerdict(ctx, playerID, verdict)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.ScreenedSystem, 0, len(models))
	for _, m := range models {
		out = append(out, appSensing.ScreenedSystem{System: m.SystemSymbol, DepthCredits: m.DepthCredits, ScreenedAt: m.ScreenedAt})
	}
	return out, nil
}

// CountOwnedProbes reports how many probe hulls the ledger accounts for.
func (p *LedgerPort) CountOwnedProbes(ctx context.Context, playerID int) (int64, error) {
	return p.repo.CountOwnedProbes(ctx, playerID)
}

// TransitionSlot advances one placement, applying any field writes atomically
// with the state flip.
//
// A lost optimistic-transition race is translated to the engine's own
// ErrSlotClaimed sentinel, which is the whole point of this mapping: the engine
// must be able to tell routine contention (skip the placement) from a ledger
// that is refusing writes (stop), and it cannot import the persistence error to
// do it.
// The KIND is part of the placement's address: a waypoint can carry a
// MARKET row and a SPARE row at once, and they are often in the same state, so a
// transition that named only the waypoint would guard on one row and write both.
func (p *LedgerPort) TransitionSlot(
	ctx context.Context,
	playerID int,
	t appSensing.SlotTransition,
	set appSensing.SlotFields,
) error {
	err := p.repo.TransitionSlot(ctx, playerID, t.Waypoint, t.Kind, t.From, t.To, func(m *persistence.SensingSlotModel) {
		if set.AssignedShip != nil {
			m.AssignedShip = nullableString(*set.AssignedShip)
		}
		if set.PurchaseYard != nil {
			m.PurchaseYard = nullableString(*set.PurchaseYard)
		}
	})
	if errors.Is(err, persistence.ErrSlotStateConflict) {
		return fmt.Errorf("%s (%s): %w", t.Waypoint, t.Kind, appSensing.ErrSlotClaimed)
	}
	return err
}

// MarkScanned records that a parked slot was scanned, and what its prices showed.
//
// It writes the freshness stamp and the smoothed spread and nothing else. A slot
// that no longer exists is an error rather than an upsert — the repository will
// not conjure a placement row from a scan.
//
// WHY IT IS SAFE TO RUN CONCURRENTLY WITH THE RECONCILE: the write sets are
// disjoint BY COLUMN. last_scan_at and spread_ewma have exactly one
// writer — this one — and every other writer of sensing_slots names only the
// columns it owns: a transition writes state (plus the hull or yard it actually
// changed), a screen re-declaration writes what it measured, a stand-down writes
// which hull is standing where. Whatever rows these paths meet on, and in
// whatever order they commit, none of them can carry a scan back to a stale
// value. See sensingSlotMetadataUpdateColumns in the repository for the
// full ownership table.
//
// It does NOT rest on the kind-based separation — the rotation admitting only
// PARKED MARKET/YARD slots while the only PARKED→X transition skips non-SPARE
// kinds, so the two rarely meet on a ROW. That separation is worth keeping as
// defence in depth, but it holds only for as long as nobody transitions a parked
// market, which the slot reaper does by design.
func (p *LedgerPort) MarkScanned(ctx context.Context, playerID int, waypoint, kind string, at time.Time, spreadEWMA float64) error {
	return p.repo.MarkScanned(ctx, playerID, waypoint, kind, at, spreadEWMA)
}

// MarkScanAttempted records that the rotation spent this slot's turn on it and
// the market-scan budget declined, so no market data was written.
//
// It writes the PACING clock only, leaving the freshness stamp where it was —
// the two-clock split. Same concurrency argument as MarkScanned above:
// last_scan_attempt_at has exactly one writer (this path) and no other writer of
// sensing_slots names it.
func (p *LedgerPort) MarkScanAttempted(ctx context.Context, playerID int, waypoint, kind string, at time.Time) error {
	return p.repo.MarkScanAttempted(ctx, playerID, waypoint, kind, at)
}

// Systems returns every system row the player holds, carrying the uncharted
// count and the charting errands the expansion engine drives off.
//
// The crew hulls past the first are a SECOND read, joined here on the system
// symbol: they are keyed on the hull rather than on the system, which is what
// makes "a probe is on one errand" a property of the key instead of a rule the
// writers have to keep.
func (p *LedgerPort) Systems(ctx context.Context, playerID int) ([]appSensing.ExpandSystem, error) {
	models, err := p.repo.Systems(ctx, playerID)
	if err != nil {
		return nil, err
	}
	extras, err := p.repo.ExtraSeeds(ctx, playerID)
	if err != nil {
		return nil, err
	}
	crews := make(map[string][]appSensing.SeedErrand, len(extras))
	for _, extra := range extras {
		crews[extra.SystemSymbol] = append(crews[extra.SystemSymbol], appSensing.SeedErrand{
			Ship: extra.ShipSymbol, State: extra.SeedState,
		})
	}
	out := make([]appSensing.ExpandSystem, 0, len(models))
	for _, m := range models {
		out = append(out, appSensing.ExpandSystem{
			System:         m.SystemSymbol,
			Verdict:        m.Verdict,
			UnchartedCount: m.UnchartedCount,
			SeedShip:       derefString(m.SeedShip),
			SeedState:      derefString(m.SeedState),
			ExtraSeeds:     crews[m.SystemSymbol],
			CatalogKnown:   m.CatalogSyncedAt != nil,
		})
	}
	return out, nil
}

// UpsertSystem records a system's screening verdict.
//
// A screen that found the waypoint catalog known stamps that fact, so a system
// the fleet swept long before this model existed is recognised on its first
// screen instead of being sent a charting seed to rediscover a catalog already
// sitting in the database. A screen that did NOT find it known leaves the stamp
// NULL, which is what keeps the system a seed target.
func (p *LedgerPort) UpsertSystem(ctx context.Context, playerID int, record appSensing.SystemRecord) error {
	now := time.Now().UTC()
	model := persistence.SensingSystemModel{
		PlayerID:       playerID,
		SystemSymbol:   record.System,
		Verdict:        record.Verdict,
		UnchartedCount: record.UnchartedCount,
		DepthCredits:   record.DepthCredits,
		// screened_at is stamped on EVERY verdict write, PENDING included: this
		// call IS the screening, so the column answers "when was this system last
		// looked at". That is the question an operator asks of a system stuck
		// PENDING — whether the sweep is reaching it at all — and leaving the
		// column NULL beside a populated catalog_synced_at read as though nothing
		// had ever screened it.
		ScreenedAt: &now,
	}
	if record.CatalogKnown {
		model.CatalogSyncedAt = &now
	}
	return p.repo.UpsertSystem(ctx, model)
}

// StampCatalogSynced records that a system's waypoint list has been swept.
func (p *LedgerPort) StampCatalogSynced(ctx context.Context, playerID int, system string) error {
	return p.repo.StampCatalogSynced(ctx, playerID, system, time.Now().UTC())
}

// UpsertSlotMetadata declares a placement the screen wants: what the waypoint
// deals in, and how deep its book is.
//
// On a waypoint that already carries a placement it refreshes those measurements
// and leaves the placement's progress alone — see sensingSlotMetadataUpdateColumns
// for why writing state or assigned_ship from here is a money bug rather than an
// inaccuracy.
func (p *LedgerPort) UpsertSlotMetadata(ctx context.Context, playerID int, slot appSensing.SlotRecord) error {
	model, err := slotModel(playerID, slot)
	if err != nil {
		return err
	}
	return p.repo.UpsertSlotMetadata(ctx, model)
}

// UpsertSpareSlot records a hull already standing at a waypoint as a placement: a
// charting seed standing down, or a probe adopted from a retired scout post.
//
// On conflict it re-points the row at this hull and leaves the screen's
// measurements and the scan history where they are.
func (p *LedgerPort) UpsertSpareSlot(ctx context.Context, playerID int, slot appSensing.SlotRecord) error {
	model, err := slotModel(playerID, slot)
	if err != nil {
		return err
	}
	return p.repo.UpsertSpareSlot(ctx, model)
}

// slotModel maps a placement record onto its row. Both upsert variants insert the
// WHOLE row on a waypoint that has none, so they share this shape; they differ
// only in what they re-assert when one is already there.
func slotModel(playerID int, slot appSensing.SlotRecord) (persistence.SensingSlotModel, error) {
	goods, err := json.Marshal(slot.WhitelistGoods)
	if err != nil {
		return persistence.SensingSlotModel{}, fmt.Errorf("failed to encode the whitelisted goods at %q: %w", slot.Waypoint, err)
	}
	return persistence.SensingSlotModel{
		PlayerID:       playerID,
		WaypointSymbol: slot.Waypoint,
		SystemSymbol:   slot.System,
		SlotKind:       slot.Kind,
		State:          slot.State,
		AssignedShip:   nullableString(slot.AssignedShip),
		WhitelistGoods: string(goods),
		DepthCredits:   slot.DepthCredits,
	}, nil
}

// SetSeed writes a system's charting errand and only that.
func (p *LedgerPort) SetSeed(ctx context.Context, playerID int, system, shipSymbol, seedState string) error {
	return p.repo.SetSeed(ctx, playerID, system, shipSymbol, seedState)
}

// SetExtraSeed writes the errand of a charting hull past a system's first.
func (p *LedgerPort) SetExtraSeed(ctx context.Context, playerID int, system, shipSymbol, seedState string) error {
	return p.repo.SetExtraSeed(ctx, playerID, system, shipSymbol, seedState)
}

// ClearExtraSeed ends the errand of a charting hull past a system's first.
func (p *LedgerPort) ClearExtraSeed(ctx context.Context, playerID int, shipSymbol string) error {
	return p.repo.ClearExtraSeed(ctx, playerID, shipSymbol)
}

// ChartShares returns every stored charting assignment.
//
// A share whose waypoint list will not decode comes back EMPTY rather than as an
// error. An empty share is stale by the engine's own coverage test — its stops are
// owned by nobody — so the crew re-solves and overwrites it, which is the recovery
// a corrupt row wants; failing the read instead would stop every crew in the fleet
// over one bad row.
func (p *LedgerPort) ChartShares(ctx context.Context, playerID int) ([]appSensing.ChartShare, error) {
	models, err := p.repo.ChartShares(ctx, playerID)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.ChartShare, 0, len(models))
	for _, m := range models {
		var waypoints []string
		if err := json.Unmarshal([]byte(m.Waypoints), &waypoints); err != nil {
			waypoints = nil
		}
		out = append(out, appSensing.ChartShare{
			Ship: m.ShipSymbol, System: m.SystemSymbol, Waypoints: waypoints, CrewKey: m.CrewKey,
		})
	}
	return out, nil
}

// SetChartShares replaces one system's whole partition.
func (p *LedgerPort) SetChartShares(
	ctx context.Context, playerID int, system string, shares []appSensing.ChartShare,
) error {
	models := make([]persistence.SensingChartShareModel, 0, len(shares))
	for _, share := range shares {
		waypoints, err := json.Marshal(nonNilStrings(share.Waypoints))
		if err != nil {
			return fmt.Errorf("failed to encode the charting share of %q: %w", share.Ship, err)
		}
		models = append(models, persistence.SensingChartShareModel{
			PlayerID:     playerID,
			ShipSymbol:   share.Ship,
			SystemSymbol: share.System,
			Waypoints:    string(waypoints),
			CrewKey:      share.CrewKey,
			UpdatedAt:    time.Now().UTC(),
		})
	}
	return p.repo.SetChartShares(ctx, playerID, system, models)
}

// ClearChartShare drops one hull's charting assignment.
func (p *LedgerPort) ClearChartShare(ctx context.Context, playerID int, shipSymbol string) error {
	return p.repo.ClearChartShare(ctx, playerID, shipSymbol)
}

// nonNilStrings keeps an empty list encoding as `[]` rather than as `null`, so the
// column's own default and what this writes agree.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// DeleteSlot removes ONE placement row outright — the one write that hands a
// parked spare's hull from the ledger to a charting errand.
//
// The KIND is required and is a money guard: a yard can hold a SPARE
// row being released here AND a MARKET row whose probe is parked there scanning.
// Releasing by waypoint alone would delete both and drop that probe out of the
// cap while it is still flying, authorising a replacement purchase for a hull we
// already own (RULINGS #4).
func (p *LedgerPort) DeleteSlot(ctx context.Context, playerID int, waypoint, kind string) error {
	return p.repo.DeleteSlot(ctx, playerID, waypoint, kind)
}

// queuedSlots flattens ledger rows into the engine's placement view.
//
// A GOODS DECODE FAILURE YIELDS AN EMPTY LIST rather than an error, and that is
// a deliberate divergence from ExistingSlots, which treats the same failure as
// fatal. The two readers need opposite things from a row they cannot parse. The
// screen SUPPLIES goods from the recorded list, so an empty one there silently
// drops a market out of the plan and has it rediscovered forever — it must fail
// loudly. The placement view only ever asks whether a hull's coverage is
// REDUNDANT, and every reader of the field treats an empty list as "unknown",
// which can only make a hull less eligible to be moved (see
// QueuedSlot.WhitelistGoods and coveredAfterMove). Failing the whole read here
// would stop the drain — and therefore all probe buying — over a row that
// merely cannot be given away.
func queuedSlots(models []persistence.SensingSlotModel) []appSensing.QueuedSlot {
	out := make([]appSensing.QueuedSlot, 0, len(models))
	for _, m := range models {
		goods, err := decodeWhitelistGoods(m.WhitelistGoods)
		if err != nil {
			goods = nil
		}
		out = append(out, appSensing.QueuedSlot{
			Waypoint:       m.WaypointSymbol,
			System:         m.SystemSymbol,
			Kind:           m.SlotKind,
			State:          m.State,
			AssignedShip:   derefString(m.AssignedShip),
			PurchaseYard:   derefString(m.PurchaseYard),
			DepthCredits:   m.DepthCredits,
			WhitelistGoods: goods,
		})
	}
	return out
}

// nullableString maps the engine's "" back onto a NULL column. Storing an empty
// string instead would leave a slot matching the probe-count's "has a hull"
// predicate with no hull behind it, inflating the count against a released
// spare.
func nullableString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// derefString flattens a nullable column to the empty string.
func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
