// Package scouting holds the domain types for the standing scout-post system
// (sp-cxpq): a desired-state table of per-system market-freshness assignments
// that the scout_post_coordinator reconciles every tick, the way the contract
// fleet coordinator reconciles its dedicated fleet.
//
// A ScoutPost is operational state, not a rich aggregate — it mirrors the
// gate-edge / tour-telemetry idiom (a persisted row read directly), so the type
// here is a plain value plus the repository port. The coordinator owns all
// mutation (RULINGS #3, single-writer); the captain edits posts only through the
// daemon RPC that writes this table, never a file.
package scouting

import "time"

// PostKind distinguishes a permanent freshness post from a one-shot frontier
// sweep. Both are manned identically by the reconciler; they differ only in what
// happens when their tour finishes:
//   - standing posts run an infinite tour and keep a system fresh forever;
//   - sweep-once posts run a single finite tour and auto-remove once it
//     completes, freeing their hull for the next unmanned post.
//
// The "scan queue" in the acceptance criteria is exactly the set of unmanned
// sweep-once posts — the captain seeds them from the frontier census (sp-7gr2),
// so no separate queue subsystem is needed.
type PostKind string

const (
	PostKindStanding  PostKind = "standing"
	PostKindSweepOnce PostKind = "sweep_once"
)

// Valid reports whether k is a recognized post kind.
func (k PostKind) Valid() bool {
	return k == PostKindStanding || k == PostKindSweepOnce
}

// primarySlotIndex addresses a post's PRIMARY manning slot (its scalar fields);
// ExtraSlots entries are addressed by their 0-based index (sp-enry).
const primarySlotIndex = -1

// ScoutPostSlot is one manning slot BEYOND the primary of a multi-probe post
// (sp-enry): an additional probe touring a DISJOINT partition of the same
// system's markets. The Admiral model — freshness per market = a probe's circuit
// time over its partition, so the scaling lever is PARTITION SIZE, not poll rate.
// The primary slot lives in the ScoutPost scalar fields; slots 1..N-1 live here.
// Partition is the slot's frozen market tour: it is recomputed ONLY when the hull
// budget changes, and persisted so a daemon restart re-adopts the same probe onto
// the same partition without re-touring (RULINGS #2).
type ScoutPostSlot struct {
	AssignedHull          string
	TourContainerID       string
	RepositionContainerID string
	Partition             []string
}

// ScoutPost is one desired-state assignment: "keep system S scanned to within
// FreshnessTarget, using Hulls probes over Hulls disjoint market partitions".
// A single-hull post (Hulls 0 or 1) has only the PRIMARY slot — the scalar fields
// below — and is byte-identical to the pre-enry post: the primary probe tours ALL
// the system's markets. A multi-hull post additionally carries ExtraSlots and a
// per-slot frozen Partition (sp-enry).
//
// AssignedHull == "" means the PRIMARY slot is unmanned and the reconciler will
// claim an idle satellite for it on its next tick. Both the assignment and the
// tour container id are persisted so a daemon restart re-adopts the same hull onto
// the same post (RULINGS #2).
//
// RepositionContainerID (sp-s232) tracks an in-flight cross-gate RELAY that is
// jump-routing an idle satellite from another system TO this post's primary slot —
// the fleet-wide half of manning that sp-qxa4 deliberately left out. It is mutually
// exclusive with AssignedHull on the same slot: a repositioning slot is NOT manned
// (the hull is still in transit), and manning clears any relay first. Persisted so
// a daemon restart re-adopts the same relay (RULINGS #2).
type ScoutPost struct {
	ID                    int
	PlayerID              int
	SystemSymbol          string
	FreshnessTarget       time.Duration
	Kind                  PostKind
	AssignedHull          string
	TourContainerID       string
	RepositionContainerID string

	// Hulls is the probe budget N (sp-enry): the post is toured by N probes over N
	// DISJOINT market partitions, so freshness per market ≈ (M/N markets) × circuit
	// pace. 0 or 1 ⇒ single-hull (the pre-enry behavior, byte-identical). Only
	// standing posts partition; sweep-once is always single-hull (HullBudget clamps
	// it). RULINGS #5: a config/DB value, never a constant.
	Hulls int

	// PrimaryPartition is the primary slot's frozen disjoint market tour when
	// Hulls>1 (sp-enry). Empty ⇒ the slot tours ALL the system's markets — the
	// single-hull behavior — so a single-hull post never carries one.
	PrimaryPartition []string

	// ExtraSlots holds slots 1..N-1 of a multi-hull post (sp-enry). Empty for a
	// single-hull post. Persisted so a restart re-adopts each probe onto the same
	// partition without a mass re-tour (RULINGS #2).
	ExtraSlots []ScoutPostSlot

	// RespawnAttempts counts CONSECUTIVE times the reconciler has respawned this post's
	// dead tour without the tour surviving to be observed healthy (sp-py4n). The
	// reconciler respawns any dead tour every tick, so a tour that crashes on a
	// PERSISTENT non-cross-system reason would otherwise respawn-loop at tick cadence
	// forever. When this reaches the configured cap the post is PARKED for a backoff
	// window (RespawnParkedUntil) instead of respawned again; a tour that finally runs
	// healthy resets it to 0, so it caps CONSECUTIVE failures, not lifetime. Persisted
	// (RULINGS #2) so the count survives a daemon restart — a crash-loop that reset its
	// count on every restart would never cap.
	RespawnAttempts int

	// RespawnParkedUntil is the earliest time the reconciler will respawn this post's
	// tour again after the respawn cap was hit (sp-py4n). Zero ⇒ not parked. While it
	// is in the future the post is skipped by the manning passes (park-with-reason);
	// once it elapses the coordinator retries exactly once, and a still-dead tour
	// re-arms the window while a healthy one clears it. Persisted with RespawnAttempts
	// so the park survives a restart rather than the crash-loop resuming at tick cadence.
	RespawnParkedUntil time.Time

	CreatedAt time.Time
}

// HullBudget is the number of manning slots the reconciler keeps filled — the
// probe budget N, defaulting to 1 (sp-enry). Sweep-once posts are ALWAYS
// single-hull: a finite one-pass frontier sweep does not partition, and clamping
// here keeps the multi-slot completion bookkeeping out of the sweep-once path
// (which stays byte-identical to pre-enry). A zero/negative Hulls (a legacy row,
// or a caller that never set it) is also a single-hull post.
func (p *ScoutPost) HullBudget() int {
	if p.Kind == PostKindSweepOnce {
		return 1
	}
	if p.Hulls < 1 {
		return 1
	}
	return p.Hulls
}

// Slots returns a mutable handle for each MATERIALIZED slot: the primary slot plus
// one per ExtraSlots entry (sp-enry). A single-hull post yields exactly one handle
// (the primary), so every pre-enry reconcile path is unchanged. The count can be
// less than HullBudget() before the partitioner has materialized the extra slots;
// the reconciler's partition pass grows ExtraSlots to HullBudget()-1.
func (p *ScoutPost) Slots() []ScoutSlotRef {
	refs := make([]ScoutSlotRef, 0, 1+len(p.ExtraSlots))
	refs = append(refs, ScoutSlotRef{post: p, index: primarySlotIndex})
	for i := range p.ExtraSlots {
		refs = append(refs, ScoutSlotRef{post: p, index: i})
	}
	return refs
}

// MannedHulls returns the ship symbols currently manning this post, across all
// slots (sp-enry). Order is primary-first, then extra slots in index order.
func (p *ScoutPost) MannedHulls() []string {
	var hulls []string
	for _, s := range p.Slots() {
		if h := s.AssignedHull(); h != "" {
			hulls = append(hulls, h)
		}
	}
	return hulls
}

// MannedCount is how many of the post's slots currently have a hull (sp-enry).
func (p *ScoutPost) MannedCount() int {
	return len(p.MannedHulls())
}

// IsFullyManned reports whether every one of the post's HullBudget() slots has a
// hull (sp-enry) — the multi-hull generalization of "manned". A single-hull post
// is fully manned exactly when its primary slot is manned.
func (p *ScoutPost) IsFullyManned() bool {
	return p.MannedCount() == p.HullBudget()
}

// ScoutSlotRef is a uniform, mutable handle onto one manning slot of a post
// (sp-enry). index == primarySlotIndex addresses the PRIMARY slot (the post's
// scalar fields + PrimaryPartition); index >= 0 addresses ExtraSlots[index]. Reads
// and writes go THROUGH to the backing store, so a single-hull post (only the
// primary slot) persists byte-identically to the pre-enry scalar layout.
type ScoutSlotRef struct {
	post  *ScoutPost
	index int
}

// IsPrimary reports whether this handle addresses the post's primary slot.
func (r ScoutSlotRef) IsPrimary() bool { return r.index == primarySlotIndex }

// Index is the slot's position: primarySlotIndex (-1) for the primary, else its
// ExtraSlots index. Used to scope per-slot bookkeeping (e.g. reposition backoff)
// so a single-hull post keeps the pre-enry, un-suffixed key.
func (r ScoutSlotRef) Index() int { return r.index }

// AssignedHull returns the hull manning this slot, or "" if unmanned.
func (r ScoutSlotRef) AssignedHull() string {
	if r.index == primarySlotIndex {
		return r.post.AssignedHull
	}
	return r.post.ExtraSlots[r.index].AssignedHull
}

// SetAssignedHull records (or clears, with "") the hull manning this slot.
func (r ScoutSlotRef) SetAssignedHull(v string) {
	if r.index == primarySlotIndex {
		r.post.AssignedHull = v
		return
	}
	r.post.ExtraSlots[r.index].AssignedHull = v
}

// TourContainerID returns this slot's tour worker container id, or "".
func (r ScoutSlotRef) TourContainerID() string {
	if r.index == primarySlotIndex {
		return r.post.TourContainerID
	}
	return r.post.ExtraSlots[r.index].TourContainerID
}

// SetTourContainerID records (or clears) this slot's tour worker container id.
func (r ScoutSlotRef) SetTourContainerID(v string) {
	if r.index == primarySlotIndex {
		r.post.TourContainerID = v
		return
	}
	r.post.ExtraSlots[r.index].TourContainerID = v
}

// RepositionContainerID returns this slot's in-flight relay container id, or "".
func (r ScoutSlotRef) RepositionContainerID() string {
	if r.index == primarySlotIndex {
		return r.post.RepositionContainerID
	}
	return r.post.ExtraSlots[r.index].RepositionContainerID
}

// SetRepositionContainerID records (or clears) this slot's relay container id.
func (r ScoutSlotRef) SetRepositionContainerID(v string) {
	if r.index == primarySlotIndex {
		r.post.RepositionContainerID = v
		return
	}
	r.post.ExtraSlots[r.index].RepositionContainerID = v
}

// Partition returns this slot's frozen disjoint market tour (sp-enry). Empty ⇒ the
// slot tours ALL the system's markets (the single-hull default).
func (r ScoutSlotRef) Partition() []string {
	if r.index == primarySlotIndex {
		return r.post.PrimaryPartition
	}
	return r.post.ExtraSlots[r.index].Partition
}

// IsManned reports whether a hull is currently assigned to this post's PRIMARY
// slot. A post with a reposition relay in flight on the primary is NOT manned — its
// satellite is still crossing gates. Retained for the pre-enry single-hull callers;
// multi-hull reconcile logic works per ScoutSlotRef instead.
func (p *ScoutPost) IsManned() bool {
	return p.AssignedHull != ""
}

// IsRepositioning reports whether a cross-gate relay is currently jump-routing a
// satellite toward this post's PRIMARY slot (sp-s232). Retained for the pre-enry
// single-hull callers; multi-hull reconcile logic works per ScoutSlotRef instead.
func (p *ScoutPost) IsRepositioning() bool {
	return p.RepositionContainerID != ""
}
