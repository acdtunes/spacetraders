package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/buffer"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-rxrg: the depot warehouse buffer selector must GATE candidate goods on hub-contract-membership,
// local-production, and source-distance BEFORE the reward knapsack ranks them, so a locally-produced
// non-contract good (the DRUGS@J58 case, modeled below) is never warehoused. These tests drive
// the LIVE selection path (depotWarehouseTargetUnits) and the supporting resolvers.

// j58Coords models the J58 scenario's geography: the J58 warehouse waypoint, DRUGS's OWN co-located
// source (J58 EXPORTS DRUGS, so its cheapest source IS J58 — distance 0), and a remote in-system
// ELECTRONICS source. Cross-system sources resolve ok=false here: their reach is decided by the
// system check, not coordinates.
func j58Coords(w string) (float64, float64, bool) {
	switch w {
	case "X1-VB74-J58":
		return 0, 0, true
	case "X1-VB74-ELEC":
		return 30, 0, true
	}
	return 0, 0, false
}

// j58Candidates is the mined receipt-demand set at J58: DRUGS and ELECTRONICS, which the gates must
// exclude, plus two genuine J58 contract goods the buffer should keep.
func j58Candidates() []persistence.DemandCandidate {
	return []persistence.DemandCandidate{
		// DRUGS: NOT a J58 contract good, and J58 EXPORTS it (co-located source) — fails all three gates.
		{Good: "DRUGS", ContractCount: 2, MaxContractUnits: 40, ForeignSystem: "X1-VB74", ForeignMarket: "X1-VB74-J58", ContractRewardPerUnit: 500},
		// ELECTRONICS: NOT a J58 contract good, remotely sourced in-system — fails gate 1 alone.
		{Good: "ELECTRONICS", ContractCount: 2, MaxContractUnits: 40, ForeignSystem: "X1-VB74", ForeignMarket: "X1-VB74-ELEC", ContractRewardPerUnit: 500},
		// ASSAULT_RIFLES: a J58 contract good, cross-system source, not locally produced — passes all.
		{Good: "ASSAULT_RIFLES", ContractCount: 3, MaxContractUnits: 40, ForeignSystem: "X9-FAR", ForeignMarket: "X9-FAR-S1", ContractRewardPerUnit: 5000},
		// POLYNUCLEOTIDES: a J58 contract good, cross-system source — a second survivor, so the gated
		// set never falls below the cold-start floor (which would resurrect the static DRUGS seed).
		{Good: "POLYNUCLEOTIDES", ContractCount: 3, MaxContractUnits: 40, ForeignSystem: "X9-FAR", ForeignMarket: "X9-FAR-S2", ContractRewardPerUnit: 400},
	}
}

// j58GateContext is the resolved J58 hub data: its real contract goods (DRUGS + ELECTRONICS absent),
// its locally-exported DRUGS, and the gate-3 floor.
func j58GateContext() bufferGateContext {
	return bufferGateContext{
		gate:               buffer.Gate{MinExternalSourceDistance: 25},
		hubContractGoods:   map[string]int{"ASSAULT_RIFLES": 3, "POLYNUCLEOTIDES": 3, "MEDICINE": 2},
		hubLocalProduction: map[string]bool{"DRUGS": true},
	}
}

// TestDepotWarehouseTargetUnits_DropsLocalNonContractGoodsKeepsRemoteContractGood is the sp-rxrg
// anchor: re-running the depot warehouse selection on a J58-like fixture drops the locally-produced /
// non-contract goods (DRUGS, ELECTRONICS) while a remotely-sourced J58 contract good (ASSAULT_RIFLES)
// still buffers. Capacity fits every survivor, so a dropped good was GATED, not knapsack-evicted.
func TestDepotWarehouseTargetUnits_DropsLocalNonContractGoodsKeepsRemoteContractGood(t *testing.T) {
	miner := &fakeReceiptMiner{rows: j58Candidates()}

	targets := depotWarehouseTargetUnits(
		context.Background(), miner, 200 /*fits every survivor: 3 × 40*/, "X1-VB74", "X1-VB74-J58",
		j58Coords, j58GateContext(), 3, nil,
	)

	require.NotContains(t, targets, "DRUGS",
		"DRUGS is exported at J58 and not a J58 contract good — it must never be warehoused (the incident)")
	require.NotContains(t, targets, "ELECTRONICS",
		"ELECTRONICS is not a J58 contract good — it must never be warehoused")
	require.Contains(t, targets, "ASSAULT_RIFLES",
		"a remotely-sourced J58 contract good must STILL buffer (the regression guard)")
}

// TestDepotWarehouseTargetUnits_EachGateIsLoadBearingOnTheLivePath is the live-path mutation proof:
// starting from a contracted, remote, non-local ASSAULT_RIFLES that DOES buffer, disabling any ONE
// gate's passing condition excludes it — while a stable contracted survivor (POLYNUCLEOTIDES) stays,
// so gating is selective, never a wholesale drop or a cold-start fallback.
func TestDepotWarehouseTargetUnits_EachGateIsLoadBearingOnTheLivePath(t *testing.T) {
	survivor := persistence.DemandCandidate{Good: "POLYNUCLEOTIDES", ContractCount: 3, MaxContractUnits: 40, ForeignSystem: "X9-FAR", ForeignMarket: "X9-FAR-S2", ContractRewardPerUnit: 400}
	remoteRifles := persistence.DemandCandidate{Good: "ASSAULT_RIFLES", ContractCount: 3, MaxContractUnits: 40, ForeignSystem: "X9-FAR", ForeignMarket: "X9-FAR-S1", ContractRewardPerUnit: 5000}
	colocatedRifles := remoteRifles
	colocatedRifles.ForeignSystem = "X1-VB74"
	colocatedRifles.ForeignMarket = "X1-VB74-J58" // co-located with the hub: distance 0

	contracted := map[string]int{"ASSAULT_RIFLES": 3, "POLYNUCLEOTIDES": 3}
	run := func(candidates []persistence.DemandCandidate, gateCtx bufferGateContext) map[string]int {
		return depotWarehouseTargetUnits(context.Background(), &fakeReceiptMiner{rows: candidates}, 200,
			"X1-VB74", "X1-VB74-J58", j58Coords, gateCtx, 3, nil)
	}

	// Precondition: all three gates satisfied -> ASSAULT_RIFLES buffers.
	base := run([]persistence.DemandCandidate{remoteRifles, survivor},
		bufferGateContext{gate: buffer.Gate{MinExternalSourceDistance: 25}, hubContractGoods: contracted})
	require.Contains(t, base, "ASSAULT_RIFLES", "precondition: a contracted, remote, non-local good buffers")

	cases := []struct {
		name       string
		candidates []persistence.DemandCandidate
		gateCtx    bufferGateContext
	}{
		{
			"gate 1 removed: ASSAULT_RIFLES not contracted to this hub",
			[]persistence.DemandCandidate{remoteRifles, survivor},
			bufferGateContext{gate: buffer.Gate{MinExternalSourceDistance: 25}, hubContractGoods: map[string]int{"POLYNUCLEOTIDES": 3}},
		},
		{
			"gate 2 removed: the hub exports ASSAULT_RIFLES",
			[]persistence.DemandCandidate{remoteRifles, survivor},
			bufferGateContext{gate: buffer.Gate{MinExternalSourceDistance: 25}, hubContractGoods: contracted, hubLocalProduction: map[string]bool{"ASSAULT_RIFLES": true}},
		},
		{
			"gate 3 removed: ASSAULT_RIFLES nearest source is co-located",
			[]persistence.DemandCandidate{colocatedRifles, survivor},
			bufferGateContext{gate: buffer.Gate{MinExternalSourceDistance: 25}, hubContractGoods: contracted},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			targets := run(testCase.candidates, testCase.gateCtx)
			require.NotContains(t, targets, "ASSAULT_RIFLES", testCase.name)
			require.Contains(t, targets, "POLYNUCLEOTIDES",
				"gating stays selective — the contracted remote survivor is unaffected")
		})
	}
}

// TestDepotWarehouseTargetUnits_EmptyContractMembershipFailsOpen pins the deploy-safety rule: a hub
// with NO resolvable contract history (empty or nil membership — a thin universe or a transient read
// gap) must still buffer per gates 2+3, never nothing. A strict "empty => drop all" would EMPTY a
// warehouse on a mere data gap.
func TestDepotWarehouseTargetUnits_EmptyContractMembershipFailsOpen(t *testing.T) {
	miner := &fakeReceiptMiner{rows: []persistence.DemandCandidate{
		{Good: "ASSAULT_RIFLES", ContractCount: 3, MaxContractUnits: 40, ForeignSystem: "X9-FAR", ForeignMarket: "X9-FAR-S1", ContractRewardPerUnit: 5000},
	}}
	for _, membership := range []map[string]int{nil, {}} {
		targets := depotWarehouseTargetUnits(context.Background(), miner, 200, "X1-VB74", "X1-VB74-J58",
			j58Coords, bufferGateContext{gate: buffer.Gate{MinExternalSourceDistance: 25}, hubContractGoods: membership}, 3, nil)
		require.Contains(t, targets, "ASSAULT_RIFLES",
			"empty/nil membership is a data gap: gate 1 fails open so a thin history never empties the warehouse")
	}
}

// TestLocalProductionGoods_IncludesExportsAndExchangesExcludesImports pins the gate-2 signal
// resolution: a hub's EXPORT and EXCHANGE goods are local production (buy on-site), an IMPORT good is
// consumed (not produced) and stays a buffer candidate, and an unscanned hub fails open (nil set).
func TestLocalProductionGoods_IncludesExportsAndExchangesExcludesImports(t *testing.T) {
	mkt, err := market.NewMarket("X1-VB74-J58", []market.TradeGood{
		*mustTradeGood(t, "DRUGS", market.TradeTypeExport),
		*mustTradeGood(t, "FUEL", market.TradeTypeExchange),
		*mustTradeGood(t, "ASSAULT_RIFLES", market.TradeTypeImport),
	}, time.Now())
	require.NoError(t, err)

	produced := localProductionGoods(mkt)
	require.True(t, produced["DRUGS"], "an EXPORT good is produced locally")
	require.True(t, produced["FUEL"], "an EXCHANGE good is traded on-site (buy locally, do not buffer)")
	require.False(t, produced["ASSAULT_RIFLES"], "an IMPORT good is CONSUMED, not produced — still a buffer candidate")
	require.Nil(t, localProductionGoods(nil), "an unscanned hub yields no local-production set (gate 2 fails open)")
}

func mustTradeGood(t *testing.T, symbol string, tradeType market.TradeType) *market.TradeGood {
	t.Helper()
	good, err := market.NewTradeGood(symbol, nil, nil, 100, 100, 10, tradeType)
	require.NoError(t, err)
	return good
}

// TestResolveDepotBufferMinSourceDistance_LiveOverridesDefault pins the gate-3 threshold's
// live>default resolution: a positive value on the active contract-coordinator config wins, and a
// zero/absent/nil config defers to the documented default (the tune revert-to-default semantics).
func TestResolveDepotBufferMinSourceDistance_LiveOverridesDefault(t *testing.T) {
	require.Equal(t, DepotBufferMinSourceDistanceDefault, resolveDepotBufferMinSourceDistance(nil),
		"a nil live config defers to the documented default")
	require.Equal(t, DepotBufferMinSourceDistanceDefault,
		resolveDepotBufferMinSourceDistance(map[string]interface{}{}),
		"an empty live config defers to the documented default")
	require.Equal(t, DepotBufferMinSourceDistanceDefault,
		resolveDepotBufferMinSourceDistance(map[string]interface{}{depotBufferMinSourceDistanceConfigKey: 0}),
		"a zero live value is 'unset' and reverts to the documented default")
	require.Equal(t, 80,
		resolveDepotBufferMinSourceDistance(map[string]interface{}{depotBufferMinSourceDistanceConfigKey: 80}),
		"a positive live value from `tune --operation contract` wins over the default")
}
