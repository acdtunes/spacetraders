package commands

import "testing"

// THE READINESS GATES COUNT THE PARKED SENTINEL. hasPurchaser is the shared predicate; the
// same-waypoint match happens downstream, where the winning yard is known.

func TestHasPurchaser_CountsAnIdleHullOrAParkedSentinel(t *testing.T) {
	cases := []struct {
		name string
		obs  Observation
		want bool
	}{
		{"an idle hull", Observation{HasIdlePurchaser: true}, true},
		{"the sentinel standing at a yard", Observation{YardSentinelYard: "X1-HQ-YARD"}, true},
		{"both", Observation{HasIdlePurchaser: true, YardSentinelYard: "X1-HQ-YARD"}, true},
		{"a sentinel that exists but is not parked", Observation{YardSentinelSymbol: "SENTINEL-1"}, false},
		{"nothing free at all", Observation{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasPurchaser(tc.obs); got != tc.want {
				t.Fatalf("hasPurchaser = %v, want %v", got, tc.want)
			}
		})
	}
}

// sentinelStallObs: no hauler yet, nothing idle, and the pivot unavailable (frigate mid-tour).
func sentinelStallObs() Observation {
	obs := incomeObs()
	obs.CommandFrigateID = "FRIGATE-1"
	obs.HasIdlePurchaser = false
	obs.CommandFrigateOnTrade = true
	obs.CommandFrigateIdle = false
	obs.FrigateCargoEmpty = true
	obs.BatchContractRunning = true // isolate: don't also launch the coordinator
	obs.ProbeCount = 3
	obs.ProbesScouting = 3
	obs.YardSentinelSymbol = "SENTINEL-1"
	return obs
}

// The parked sentinel unblocks the buy and the earner is never touched.
func TestBootstrap_Hauler_ParkedSentinelBuysWithoutDivertingTheEarner(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "X1-HQ-YARD", readable: true}
	obs := sentinelStallObs()
	obs.YardSentinelYard = "X1-HQ-YARD"
	h := pivotHandler(obs, ret, acq, &fakeFrigateLoop{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Blocker == "no_purchaser" {
		t.Fatalf("a sentinel docked at the yard IS a purchaser — the buy must not block on no_purchaser")
	}
	if acq.buys != 1 || res.HaulersBought != 1 {
		t.Fatalf("the hauler must be bought; buys=%d HaulersBought=%d blocker=%q", acq.buys, res.HaulersBought, res.Blocker)
	}
	if len(acq.purchasers) != 1 || acq.purchasers[0] != "" {
		t.Fatalf("no purchaser may be pinned — the acquirer finds the sentinel at the winning yard; purchasers=%v", acq.purchasers)
	}
	if res.FrigatePivoted || len(ret.dedications) != 0 {
		t.Fatalf("the sole earning hull must not be diverted; FrigatePivoted=%v dedications=%v", res.FrigatePivoted, ret.dedications)
	}
}

// No idle hull, no pivot, and NO sentinel standing anywhere ⇒ still no_purchaser, still no spend.
func TestBootstrap_Hauler_StillBlocksWhenNoSentinelIsParked(t *testing.T) {
	ret := &fakeRetirer{}
	acq := &fakeHaulerAcquirer{price: 300000, yard: "X1-HQ-YARD", readable: true}
	h := pivotHandler(sentinelStallObs(), ret, acq, &fakeFrigateLoop{}) // YardSentinelYard unset

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Blocker != "no_purchaser" {
		t.Fatalf("blocker = %q, want no_purchaser", res.Blocker)
	}
	if acq.buys != 0 || len(ret.dedications) != 0 {
		t.Fatalf("nothing may be bought and nothing diverted; buys=%d dedications=%v", acq.buys, ret.dedications)
	}
}

// THE REPLACE-ON-LOSS BUY GETS THE SAME PURCHASER.
func TestBootstrap_ProbeReplacement_ParkedSentinelUnblocksTheBuy(t *testing.T) {
	obs := freshDataObs()
	obs.ProbeCount = probeTarget - 1
	obs.ProbesScouting = probeTarget - 1
	obs.HasIdlePurchaser = false
	obs.YardSentinelSymbol = "SENTINEL-1"
	obs.YardSentinelYard = "X1-HQ-YARD"
	h, spies := spiedHandler(obs, &fakeHandoff{})

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Blocker == "no_purchaser" {
		t.Fatalf("the parked sentinel must unblock the replacement buy")
	}
	if spies.probes.buys != 1 {
		t.Fatalf("the lost scout must be replaced; probe buys=%d blocker=%q", spies.probes.buys, res.Blocker)
	}
}
