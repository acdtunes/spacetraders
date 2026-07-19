package commands

import (
	"context"
	"testing"
)

// sp-nkqn: routine early-game contract-hauler scaling must be GUARD-GATED AUTO, not
// captain-approval-gated. The capacity reconciler already EMITS its tier-4 contract-delivery
// capital demand into the autosizer via ContractDeliveryDemandBridge (registered as a demand
// provider); the ONLY thing keeping it dormant is that the "contract_delivery" class is outside
// the set classDisabled recognizes ("unknown class: never act"). Arming it — behind an OPT-IN
// flag defaulting OFF, exactly like the explorer/warehouse classes — routes the routine buy
// through sp-1txd's SINGLE money-guard stack (reserve floor + <=25% treasury + fleet ceiling +
// per-tick cap + realized-$/hr), so the GUARDS are the gate (RULINGS #6), not a human approval.
// Strategic capital (heavy tranche / new abroad system / gate go/no-go) is NOT in this class and
// stays gated elsewhere.

// The contract_delivery class is OPT-IN: classDisabled is true (skipped) unless explicitly armed.
// Default OFF is the byte-identical safe default — a bare deploy keeps the class dormant exactly
// as today, so routine scaling is a conscious, config-armed behaviour (no dark-shipping a buy).
func TestContractDelivery_ClassDisabled_OptInDefaultOff(t *testing.T) {
	disarmed := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{})
	if !disarmed.classDisabled(HullClassContractDelivery) {
		t.Fatalf("contract_delivery must be DISABLED by default (opt-in) — nothing boot-arms routine scaling")
	}
	armed := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{ContractDeliveryHullsEnabled: true})
	if armed.classDisabled(HullClassContractDelivery) {
		t.Fatalf("contract_delivery must be ENABLED once contract_delivery_hulls_enabled=true")
	}
}

// Resolve fills the class's protective defaults and — crucially — leaves it DISARMED. Nothing
// boot-arms routine contract-hauler buys; the treasury-pct default is the <=25% rule the bead
// mandates for the routine class (unlike lights, which are exempt from the big-ticket %-rule).
func TestContractDelivery_ResolveDefaults_NothingBootArms(t *testing.T) {
	cfg := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{})
	if cfg.ContractDeliveryHullsEnabled {
		t.Fatalf("contract_delivery_hulls_enabled must default FALSE (disarmed) — nothing boot-arms routine scaling")
	}
	if cfg.FleetCeilingContractDelivery != defaultFleetCeilingContractDelivery {
		t.Errorf("contract_delivery class ceiling default = %d, want %d", cfg.FleetCeilingContractDelivery, defaultFleetCeilingContractDelivery)
	}
	if cfg.ContractDeliveryTreasuryPctPerPurchase != defaultContractDeliveryTreasuryPctPerPurchase {
		t.Errorf("contract_delivery treasury pct default = %d, want %d (the <=25%% rule bites the routine class)", cfg.ContractDeliveryTreasuryPctPerPurchase, defaultContractDeliveryTreasuryPctPerPurchase)
	}
	if cfg.ContractDeliveryTreasuryPctPerPurchase != 25 {
		t.Errorf("contract_delivery treasury pct must be 25 (RULINGS #6 25%% rule), got %d", cfg.ContractDeliveryTreasuryPctPerPurchase)
	}
	if cfg.ShipTypeContractDelivery != defaultShipTypeContractDelivery {
		t.Errorf("contract_delivery ship type default = %q, want %q", cfg.ShipTypeContractDelivery, defaultShipTypeContractDelivery)
	}
}

// classGuardConfig hands the contract_delivery class its REAL guard bounds — a light-hauler ship
// type, a conservative class ceiling, and the 25% affordability rule. maxPrice defaults to 0 (no
// absolute cap; the premium-over-cheapest ceiling + reserve floor + 25% rule are the price
// protections, exactly as for the light/worker pool this frame class matches).
func TestContractDelivery_ClassGuardConfig_RealBounds(t *testing.T) {
	cfg := resolveFleetAutosizerConfig(&RunFleetAutosizerCoordinatorCommand{ContractDeliveryHullsEnabled: true})
	shipType, ceiling, maxPrice, treasuryPct := classGuardConfig(HullClassContractDelivery, cfg)
	if shipType != defaultShipTypeContractDelivery {
		t.Errorf("contract_delivery ship type = %q, want %q", shipType, defaultShipTypeContractDelivery)
	}
	if ceiling != defaultFleetCeilingContractDelivery {
		t.Errorf("contract_delivery class ceiling = %d, want %d", ceiling, defaultFleetCeilingContractDelivery)
	}
	if maxPrice != 0 {
		t.Errorf("contract_delivery max price = %d, want 0 (no absolute cap; premium ceiling applies)", maxPrice)
	}
	if treasuryPct != 25 {
		t.Errorf("contract_delivery treasury pct = %d, want 25 (the <=25%% rule must bite the routine buy)", treasuryPct)
	}
}

// End-to-end at the coordinator: a DISARMED contract_delivery class is skipped entirely (its
// provider — the bridge — is never even consulted), so routine scaling stays dormant exactly as
// today. Once ARMED the class runs and the provider is consulted. This is the reconcile-level
// proof that the opt-in flag gates the WHOLE class, the belt to the byte-identical default.
func TestContractDelivery_Reconcile_DisarmedSkipsClassEntirely(t *testing.T) {
	// A provider that WOULD want to buy if ever asked (readable demand, shortfall, pool empty).
	spy := &spyContractDeliveryProvider{}

	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.AddDemandProvider(spy)

	// DISARMED: the class is skipped, the provider is never consulted.
	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1"}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("DISARMED contract_delivery class must be SKIPPED (provider never called), got %d calls", spy.calls)
	}

	// ARMED: the class runs and the provider is consulted (no purchaser wired, so nothing is spent).
	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1", ContractDeliveryHullsEnabled: true}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if spy.calls == 0 {
		t.Fatalf("ARMED contract_delivery class must be evaluated (provider consulted at least once)")
	}
}

// The armed contract_delivery buy runs the FULL money-guard stack — it is NOT explorer-exempt.
// Every guard the bead requires still BITES: insufficient solvency (reserve floor), >25% treasury,
// over the absorption/fleet ceiling, no measured demand, and an unreadable realized rate each
// BLOCK the buy; only a request that clears every guard is approved. This proves arming does not
// weaken any money guard (RULINGS #4/#6) — the guards ARE the gate.
func TestContractDelivery_Guards_StillBite(t *testing.T) {
	// A candidate that clears every guard (routine light hauler, ample treasury, measured rate).
	base := PurchaseRequest{
		Class:    HullClassContractDelivery,
		ShipType: defaultShipTypeContractDelivery,

		Shortfall: 2,

		CurrentClassCount: 1,
		ClassCeiling:      defaultFleetCeilingContractDelivery,
		CurrentTotalCount: 5,
		TotalCeiling:      defaultFleetCeilingTotal,

		PurchasesThisTick: 0,
		PerTickCap:        1,

		Price:              80000,
		PriceReadable:      true,
		CheapestKnownPrice: 80000,
		MaxPriceClass:      0, // no absolute cap; premium ceiling applies
		MaxPremiumPct:      defaultMaxPremiumOverCheapestPct,

		HoursToEraEnd:  100,
		EraReadable:    true,
		EraCutoffHours: 3,
		PaybackSafety:  0.5,

		MarginalRate:  5000,
		RateFloor:     3500,
		RateReadable:  true,
		RateDeclining: false,

		LiveTreasury:      2000000,
		TreasuryReadable:  true,
		ReserveAbsolute:   50000,
		ReservePct:        40,
		MarginOverFloor:   200000,
		TreasuryPctPerBuy: 25,

		APIUtilPct:      10,
		APIUtilReadable: true,
		APIUtilCeiling:  85,
	}

	if d := EvaluateGuards(base); !d.Approved {
		t.Fatalf("baseline contract_delivery buy must be APPROVED when every guard clears, blocked by %s: %s", d.BlockedBy, d.Arithmetic())
	}

	cases := []struct {
		name string
		want GuardName
		mut  func(r *PurchaseRequest)
	}{
		{"no measured demand", GuardDemand, func(r *PurchaseRequest) { r.Shortfall = 0 }},
		{"over fleet/absorption ceiling", GuardFleetCeiling, func(r *PurchaseRequest) { r.CurrentClassCount = r.ClassCeiling }},
		// Reserve floor binds while the 25% rule still passes: treasury 400k (25% = 100k >= price 80k
		// → treasury_pct ok), effective floor = min(absolute 200k, 40% × 400k = 160k) = 160k →
		// spendable 240k < price+margin 280k → treasury_floor is the blocker.
		{"insufficient solvency (reserve floor)", GuardTreasuryFloor, func(r *PurchaseRequest) { r.LiveTreasury = 400000; r.ReserveAbsolute = 200000 }},
		// 25% rule binds first: treasury 200k → 25% cap 50k < price 80k.
		{"over 25% of treasury", GuardTreasuryPct, func(r *PurchaseRequest) { r.LiveTreasury = 200000; r.Price = 80000 }},
		// Absorption cap: a readable marginal rate BELOW the realized-$/hr floor stops the buy (the
		// era-payback proof still clears at this rate, so realized_rate is the first blocker).
		{"marginal rate below absorption floor", GuardRealizedRate, func(r *PurchaseRequest) { r.MarginalRate = 3000 }},
		// Fail-closed on an UNREADABLE realized rate: the income proof cannot be made, so the buy is
		// refused (era_payback is the first of the two income guards to fail on an unreadable rate).
		{"unreadable realized rate (fail-closed)", GuardEraPayback, func(r *PurchaseRequest) { r.RateReadable = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mut(&req)
			d := EvaluateGuards(req)
			if d.Approved {
				t.Fatalf("%s: buy must be BLOCKED but was approved: %s", tc.name, d.Arithmetic())
			}
			if d.BlockedBy != tc.want {
				t.Errorf("%s: blocked by %s, want %s (%s)", tc.name, d.BlockedBy, tc.want, d.Arithmetic())
			}
		})
	}
}

// spyContractDeliveryProvider is a minimal contract_delivery demand provider that records how many
// times the coordinator consults it, so a test can prove the DISARMED class is skipped before any
// demand read. When consulted it reports a readable shortfall (as the live bridge would once the
// reconciler has emitted a gap).
type spyContractDeliveryProvider struct {
	calls int
}

func (s *spyContractDeliveryProvider) Class() HullClass { return HullClassContractDelivery }
func (s *spyContractDeliveryProvider) Demand(_ context.Context, _ int, _ DemandParams) (ClassDemand, error) {
	s.calls++
	return ClassDemand{
		Class:        HullClassContractDelivery,
		Demand:       2,
		Current:      0,
		MarginalRate: 5000,
		FleetAvgRate: 4000,
		RateReadable: true,
		Readable:     true,
		Reason:       "spy: readable contract-delivery gap",
	}, nil
}
