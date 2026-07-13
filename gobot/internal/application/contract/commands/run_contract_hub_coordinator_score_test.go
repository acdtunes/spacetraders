package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Demand EWMA (smoothing is MANDATORY — the raw contract signal is thin/noisy) ---

// A brand-new good seen in ONE recent contract earns only a tiny smoothed weight, while a good
// that has recurred across many contracts carries most of the demand. This is the property that
// stops single-contract noise from moving placement.
func TestComputeDemandWeights_SingleNewGoodIsHeavilySmoothed(t *testing.T) {
	contracts := make([]ContractDemandRecord, 0, 21)
	for i := 0; i < 20; i++ {
		contracts = append(contracts, ContractDemandRecord{Goods: []string{"OLD"}, PaymentOnFulfilled: 1000})
	}
	contracts = append(contracts, ContractDemandRecord{Goods: []string{"NEW"}, PaymentOnFulfilled: 1000})

	w := computeDemandWeights(contracts, 23) // half-life ~ half a 46-contract era

	assert.Greater(t, w["OLD"], w["NEW"], "a recurring good must outweigh a one-off newcomer")
	// With half-life 23 the newcomer's first observation carries ~3% of its payment.
	assert.Less(t, w["NEW"], 50.0, "a single new contract must earn only a small smoothed weight")
	assert.Greater(t, w["OLD"], 300.0, "20 recurrences must accumulate substantial weight")
}

// Recency: a good with the same payment appearing in more recent contracts weighs more than one
// last seen long ago (EWMA decays absent goods).
func TestComputeDemandWeights_RecencyDecaysAbsentGoods(t *testing.T) {
	// STALE appears only at the very start; RECENT appears only at the very end.
	contracts := []ContractDemandRecord{
		{Goods: []string{"STALE"}, PaymentOnFulfilled: 1000},
	}
	for i := 0; i < 10; i++ {
		contracts = append(contracts, ContractDemandRecord{Goods: []string{"FILLER"}, PaymentOnFulfilled: 10})
	}
	contracts = append(contracts, ContractDemandRecord{Goods: []string{"RECENT"}, PaymentOnFulfilled: 1000})

	w := computeDemandWeights(contracts, 5)
	assert.Greater(t, w["RECENT"], w["STALE"], "a recent observation must outweigh an old one of equal size")
}

func TestComputeDemandWeights_EmptyIsEmpty(t *testing.T) {
	assert.Empty(t, computeDemandWeights(nil, 23))
}

// --- buildCoverage: baseline when no hubs, else min-distance over homed hub positions ---

func TestBuildCoverage_BaselineWhenNoHomes(t *testing.T) {
	sources := []GoodSource{{Good: "G", Waypoint: "S", X: 0, Y: 0}}
	cov := buildCoverage(sources, nil, 1_000_000)
	assert.Equal(t, 1_000_000.0, cov["G"], "with no hubs, coverage is the large baseline so the first hub captures the top cluster")
}

func TestBuildCoverage_MinOverHomedPositions(t *testing.T) {
	sources := []GoodSource{{Good: "G", Waypoint: "S", X: 0, Y: 0}}
	homes := []hubPosition{{X: 100, Y: 0}, {X: 3, Y: 4}} // nearest is dist 5
	cov := buildCoverage(sources, homes, 1_000_000)
	assert.Equal(t, 5.0, cov["G"], "coverage is the min distance from any homed hub to the good's source")
}

// --- hubMarginal: greedy max-coverage / facility-location. A 2nd central hub adds ~0 marginal
// (the cluster is already covered); an OUTLIER whose source no hub is near scores high. This is
// the geometry the bead requires with no special-casing. ---

func TestHubMarginal_CentralSaturatesOutlierScoresHigh(t *testing.T) {
	weights := map[string]float64{"CENTRAL": 1, "OUTLIER": 1}
	sources := []GoodSource{
		{Good: "CENTRAL", Waypoint: "SC", X: 0, Y: 0},
		{Good: "OUTLIER", Waypoint: "SO", X: 1000, Y: 0},
	}
	// One hub already homed on the central cluster.
	coverage := buildCoverage(sources, []hubPosition{{X: 0, Y: 0}}, 1_000_000)

	central2 := hubMarginal(HubCandidate{Waypoint: "C_CENTRAL2", X: 0, Y: 0}, weights, sources, coverage)
	outlier := hubMarginal(HubCandidate{Waypoint: "C_OUTLIER", X: 1000, Y: 0}, weights, sources, coverage)

	assert.InDelta(t, 0.0, central2, 1e-9, "a 2nd hub on an already-covered cluster adds ~0 marginal (self-limiting)")
	assert.InDelta(t, 1000.0, outlier, 1e-9, "an outlier source no hub is near scores its full weighted coverage gain")
	assert.Greater(t, outlier, central2)
}

// --- planPlacements: greedy argmax, positive-marginal only, concentration caps ---

// Acceptance #3 (no all-haulers-on-one-hub): the greedy max-coverage engine NEVER stacks every
// idle hauler on the single best hub — once a hub is homed it adds zero further coverage, so
// extra haulers are not clumped onto it.
func TestPlanPlacements_DoesNotClumpAllHaulersOnOneHub(t *testing.T) {
	cmd := &RunContractHubCoordinatorCommand{MaxHaulersPerHub: 3} // generous cap; clumping must still not happen
	cfg := resolveContractHubConfig(cmd)
	scan := HubScan{
		Candidates: []HubCandidate{{Waypoint: "C1", X: 0, Y: 0}},
		Sources:    []GoodSource{{Good: "G", Waypoint: "S", X: 0, Y: 0}},
	}
	weights := map[string]float64{"G": 1000}
	toPlace := []HaulerHome{{ShipSymbol: "H1", Idle: true}, {ShipSymbol: "H2", Idle: true}, {ShipSymbol: "H3", Idle: true}}

	got := newHubTestHandler(nil, nil, nil, nil).planPlacements(cfg, scan, weights, nil, nil, toPlace)

	counts := map[string]int{}
	for _, p := range got {
		counts[p.HubWaypoint]++
	}
	assert.LessOrEqual(t, counts["C1"], 1, "must not clump all idle haulers onto the single best hub")
}

// The concentration cap is a HARD ceiling: with several demand clusters and cap=1, haulers
// spread across distinct hubs and no hub ever exceeds the cap.
func TestPlanPlacements_CapSpreadsAcrossHubsNoHubExceedsCap(t *testing.T) {
	cmd := &RunContractHubCoordinatorCommand{MaxHaulersPerHub: 1}
	cfg := resolveContractHubConfig(cmd)
	scan := HubScan{
		Candidates: []HubCandidate{
			{Waypoint: "C_A", X: 0, Y: 0},
			{Waypoint: "C_B", X: 100, Y: 0},
			{Waypoint: "C_C", X: 200, Y: 0},
		},
		Sources: []GoodSource{
			{Good: "A", Waypoint: "SA", X: 0, Y: 0},
			{Good: "B", Waypoint: "SB", X: 100, Y: 0},
			{Good: "C", Waypoint: "SC", X: 200, Y: 0},
		},
	}
	weights := map[string]float64{"A": 1000, "B": 1000, "C": 1000}
	toPlace := []HaulerHome{{ShipSymbol: "H1", Idle: true}, {ShipSymbol: "H2", Idle: true}, {ShipSymbol: "H3", Idle: true}}

	got := newHubTestHandler(nil, nil, nil, nil).planPlacements(cfg, scan, weights, nil, nil, toPlace)

	counts := map[string]int{}
	for _, p := range got {
		counts[p.HubWaypoint]++
	}
	assert.Len(t, got, 3, "three clusters can host three haulers")
	assert.Len(t, counts, 3, "haulers must spread across three distinct hubs")
	for hub, n := range counts {
		assert.LessOrEqual(t, n, cfg.MaxHaulersPerHub, "hub %s exceeded the concentration cap", hub)
	}
}

// A hub already at the cap from PERSISTED homes is excluded; a new hauler lands on the next
// eligible hub. This exercises the cap branch directly (the persisted-home occupancy path).
func TestPlanPlacements_PersistedHomesSaturatingHubExcludeIt(t *testing.T) {
	cmd := &RunContractHubCoordinatorCommand{MaxHaulersPerHub: 1}
	cfg := resolveContractHubConfig(cmd)
	scan := HubScan{
		Candidates: []HubCandidate{{Waypoint: "C_A", X: 0, Y: 0}, {Waypoint: "C_B", X: 100, Y: 0}},
		Sources:    []GoodSource{{Good: "B", Waypoint: "SB", X: 100, Y: 0}},
	}
	weights := map[string]float64{"B": 1000}
	// C_A is already full (a persisted hauler homed there). Its position does NOT cover SB, so
	// C_B is the only positive-marginal, under-cap hub for the new hauler.
	hubCounts := map[string]int{"C_A": 1}
	homedPositions := []hubPosition{{X: 0, Y: 0}}
	toPlace := []HaulerHome{{ShipSymbol: "H_NEW", Idle: true}}

	got := newHubTestHandler(nil, nil, nil, nil).planPlacements(cfg, scan, weights, homedPositions, hubCounts, toPlace)

	require.Len(t, got, 1)
	assert.Equal(t, "C_B", got[0].HubWaypoint, "a hub at cap must be excluded; the new hauler takes the next eligible hub")
}

// --- Acceptance #2: injecting a single new contract for a NEW good does NOT flip an existing
// placement decision (only shifts the ranking marginally). ---

func TestSmoothing_SingleNewContractDoesNotFlipPlacement(t *testing.T) {
	// FUEL recurs 20× (its cheapest source sits on C_NEAR); a rival good RARE would be sourced
	// at C_FAR. The established best hub for a new hauler is C_NEAR.
	scan := HubScan{
		Candidates: []HubCandidate{{Waypoint: "C_NEAR", X: 0, Y: 0}, {Waypoint: "C_FAR", X: 100, Y: 0}},
		Sources: []GoodSource{
			{Good: "FUEL", Waypoint: "SF", X: 0, Y: 0},
			{Good: "RARE", Waypoint: "SR", X: 100, Y: 0},
		},
	}
	base := make([]ContractDemandRecord, 0, 21)
	for i := 0; i < 20; i++ {
		base = append(base, ContractDemandRecord{Goods: []string{"FUEL"}, PaymentOnFulfilled: 1000})
	}
	newHauler := []HaulerHome{{ShipSymbol: "H1", Idle: true}}

	// Baseline: no RARE contract yet → C_NEAR.
	assignerBefore := &fakeHomeAssigner{}
	hBefore := newHubTestHandler(&fakeHubCandidateSource{scan: scan}, &fakeContractDemandSource{contracts: base}, &fakeHaulerHomeSource{haulers: newHauler}, assignerBefore)
	_, err := hBefore.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	require.NoError(t, err)
	require.Equal(t, "C_NEAR", assignerBefore.assigned["H1"])

	// Inject ONE new contract for the new good RARE (large payment). Smoothing must keep C_NEAR.
	withRare := append(append([]ContractDemandRecord{}, base...), ContractDemandRecord{Goods: []string{"RARE"}, PaymentOnFulfilled: 5000})
	assignerAfter := &fakeHomeAssigner{}
	hAfter := newHubTestHandler(&fakeHubCandidateSource{scan: scan}, &fakeContractDemandSource{contracts: withRare}, &fakeHaulerHomeSource{haulers: newHauler}, assignerAfter)
	_, err = hAfter.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	require.NoError(t, err)
	assert.Equal(t, "C_NEAR", assignerAfter.assigned["H1"], "a single new-good contract must not flip the established placement")
}
