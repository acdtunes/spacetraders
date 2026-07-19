package scouting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ---- HullBudget ------------------------------------------------------------

// A post that never set Hulls (0) is single-hull — the default.
func TestHullBudget_ZeroIsSingleHull(t *testing.T) {
	p := &ScoutPost{Kind: PostKindStanding}
	require.Equal(t, 1, p.HullBudget())
}

// Hulls>1 is honored for a standing post — the multi-probe budget.
func TestHullBudget_StandingHonorsBudget(t *testing.T) {
	p := &ScoutPost{Kind: PostKindStanding, Hulls: 3}
	require.Equal(t, 3, p.HullBudget())
}

// Sweep-once is ALWAYS single-hull: a one-pass frontier sweep does not partition,
// even if a caller sets a larger budget.
func TestHullBudget_SweepOnceClampedToOne(t *testing.T) {
	p := &ScoutPost{Kind: PostKindSweepOnce, Hulls: 4}
	require.Equal(t, 1, p.HullBudget())
}

// ---- Slots / ScoutSlotRef write-through --------------------------------------

// A single-hull post yields exactly one slot handle (the primary), backed by the
// scalar fields.
func TestSlots_SingleHullYieldsOnlyPrimary(t *testing.T) {
	p := &ScoutPost{Kind: PostKindStanding, AssignedHull: "SAT-1", TourContainerID: "tour-1"}
	slots := p.Slots()
	require.Len(t, slots, 1)
	require.True(t, slots[0].IsPrimary())
	require.Equal(t, "SAT-1", slots[0].AssignedHull())
	require.Equal(t, "tour-1", slots[0].TourContainerID())
}

// The primary slot handle writes THROUGH to the post's scalar fields, so a
// single-hull post persists with just the scalar fields.
func TestSlotRef_PrimaryWritesThroughToScalars(t *testing.T) {
	p := &ScoutPost{Kind: PostKindStanding}
	primary := p.Slots()[0]
	primary.SetAssignedHull("SAT-9")
	primary.SetTourContainerID("tour-9")
	primary.SetRepositionContainerID("relay-9")
	require.Equal(t, "SAT-9", p.AssignedHull)
	require.Equal(t, "tour-9", p.TourContainerID)
	require.Equal(t, "relay-9", p.RepositionContainerID)
}

// A multi-hull post yields HullBudget() handles — primary + one per ExtraSlots —
// and each extra handle writes THROUGH to its ExtraSlots entry.
func TestSlots_MultiHullYieldsPrimaryPlusExtras(t *testing.T) {
	p := &ScoutPost{
		Kind:             PostKindStanding,
		Hulls:            3,
		AssignedHull:     "SAT-0",
		PrimaryPartition: []string{"M0"},
		ExtraSlots: []ScoutPostSlot{
			{AssignedHull: "SAT-1", Partition: []string{"M1"}},
			{Partition: []string{"M2"}},
		},
	}
	slots := p.Slots()
	require.Len(t, slots, 3)

	require.Equal(t, []string{"M0"}, slots[0].Partition())
	require.Equal(t, []string{"M1"}, slots[1].Partition())
	require.Equal(t, []string{"M2"}, slots[2].Partition())

	// Man the third slot through its handle → the ExtraSlots entry updates.
	slots[2].SetAssignedHull("SAT-2")
	slots[2].SetTourContainerID("tour-2")
	require.Equal(t, "SAT-2", p.ExtraSlots[1].AssignedHull)
	require.Equal(t, "tour-2", p.ExtraSlots[1].TourContainerID)
}

// ---- Manned aggregates -------------------------------------------------------

// MannedHulls lists every slot's hull, primary-first; MannedCount and
// IsFullyManned reflect partial vs full manning against the budget.
func TestMannedAggregates_PartialAndFull(t *testing.T) {
	p := &ScoutPost{
		Kind:         PostKindStanding,
		Hulls:        3,
		AssignedHull: "SAT-0",
		ExtraSlots: []ScoutPostSlot{
			{AssignedHull: "SAT-1"},
			{}, // unmanned
		},
	}
	require.Equal(t, []string{"SAT-0", "SAT-1"}, p.MannedHulls())
	require.Equal(t, 2, p.MannedCount())
	require.False(t, p.IsFullyManned(), "2 of 3 slots manned is not fully manned")

	p.ExtraSlots[1].AssignedHull = "SAT-2"
	require.Equal(t, 3, p.MannedCount())
	require.True(t, p.IsFullyManned(), "all 3 slots manned is fully manned")
}

// A single-hull post is fully manned exactly when its primary slot is manned.
func TestIsFullyManned_SingleHull(t *testing.T) {
	p := &ScoutPost{Kind: PostKindStanding}
	require.False(t, p.IsFullyManned())
	p.AssignedHull = "SAT-1"
	require.True(t, p.IsFullyManned())
}
