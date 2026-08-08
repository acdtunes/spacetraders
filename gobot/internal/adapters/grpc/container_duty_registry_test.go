package grpc

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE DUTY GUARD. containerSpecList declares WHAT each live container type is; the Duty field
// declares WHAT IT OWNS. This file is the enforcement: a duty has exactly one owner, an owner
// that has handed its duty on is deleted, and the only way to run two engines over one duty is to
// say so out loud with a date and the bead that ends it.
//
// It is a pure function over a spec list, not a read of the global registry, so a HISTORICAL
// registry can be fed to it. That is what makes the retro-validation below possible, and the
// retro-validation is what makes the guard real rather than decorative.
//
// THE VOCABULARY IS PER ENGINE AND PER POOL, and the granularity is the whole design. Coarser
// ("sizes the fleet", "buys hulls") collides fleet growth with the contract scaler, which is the
// split the Admiral ruled correct — growth owns trade capacity, the scaler owns contract capacity.
// Finer (a duty per hull class, per knob) and the duty tracks an implementation detail, so a rename
// or a class move hides a duplicate in plain sight. Per engine, per pool, both historical incidents
// express in one vocabulary; a notch either way and one of them stops being visible.
//
// The zero value is UNSPECIFIED rather than "none" for the same reason TuneApplies' is: a type that
// never considered the question must report as undeclared, not inherit a confident answer.

// dutyFinding is one violation, carrying the duty and every type implicated so a failure NAMES
// both engines rather than saying "a duplicate exists".
type dutyFinding struct {
	duty  Duty
	types []string
	why   string
}

func (f dutyFinding) String() string {
	if len(f.types) == 0 {
		return fmt.Sprintf("duty %q: %s", f.duty, f.why)
	}
	return fmt.Sprintf("duty %q owned by [%s]: %s", f.duty, strings.Join(f.types, ", "), f.why)
}

// validateContainerDuties is the guard. It reports every violation rather than the first, so one
// run tells an engineer the whole story.
//
// The five rules, each of which is a way the replacement-without-retirement failure shows up:
//
//	R1 every registered type declares a duty — DutyUnspecified is not a value, it is a forgotten
//	   field, and a successor that forgets is invisible to R2.
//	R2 no two live types own one duty — the duplicate-engine rule itself.
//	R3 a registered type is never also declared retired — the corpse rule: retiredCommandTypes
//	   says the type is gone, containerSpecList says it is buildable, and the row wins.
//	R4 a declared overlap is dated and names the bead that ends it — an undated exemption is a
//	   permanent one.
//	R5 a declared overlap matches the live duplicate EXACTLY — so the ledger cannot outlive the
//	   overlap it excuses (that is the very rot this bead is about) and cannot silently absorb a
//	   THIRD claimant that nobody reviewed.
func validateContainerDuties(specs []ContainerSpec, overlaps map[Duty]dutyOverlap, retired map[string]bool) []dutyFinding {
	var findings []dutyFinding

	owners := map[Duty][]string{}
	for _, spec := range specs {
		if spec.Duty == DutyUnspecified {
			findings = append(findings, dutyFinding{
				duty:  DutyUnspecified,
				types: []string{spec.CommandType},
				why: "declares no duty. Every registered type states what it OWNS — DutyNone if it is a " +
					"one-shot verb or a coordinator-managed worker, a named duty if it is a standing engine",
			})
		}
		if retired[spec.CommandType] {
			findings = append(findings, dutyFinding{
				duty:  spec.Duty,
				types: []string{spec.CommandType},
				why:   "is declared retired AND still registered — the retirement is not delivered while a builder exists",
			})
		}
		if spec.Duty == DutyUnspecified || spec.Duty == DutyNone {
			continue
		}
		owners[spec.Duty] = append(owners[spec.Duty], spec.CommandType)
	}

	for duty, claimants := range owners {
		sort.Strings(claimants)
		overlap, declared := overlaps[duty]
		if len(claimants) == 1 {
			if declared {
				findings = append(findings, dutyFinding{duty: duty, types: claimants,
					why: "has a declared overlap but only ONE owner — the overlap is over; delete the ledger entry " +
						"(a stale exemption is exactly the dead surface this guard exists to stop)"})
			}
			continue
		}
		if !declared {
			findings = append(findings, dutyFinding{duty: duty, types: claimants,
				why: "is owned by more than one live container type. Retire the predecessor, or declare a dated " +
					"overlap in declaredDutyOverlaps naming the bead that ends it"})
			continue
		}
		if !sameTypeSet(overlap.Types, claimants) {
			findings = append(findings, dutyFinding{duty: duty, types: claimants,
				why: fmt.Sprintf("does not match its declared overlap [%s] — a duplicate nobody reviewed cannot "+
					"inherit an exemption written for a different pair", strings.Join(sortedCopy(overlap.Types), ", "))})
		}
		if strings.TrimSpace(overlap.Since) == "" || strings.TrimSpace(overlap.Retirement) == "" {
			findings = append(findings, dutyFinding{duty: duty, types: claimants,
				why: "declares an overlap with no date or no retirement bead — an unattributed exemption is a permanent one"})
		}
	}
	return findings
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sameTypeSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := sortedCopy(a), sortedCopy(b)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func findingsText(findings []dutyFinding) string {
	parts := make([]string, 0, len(findings))
	for _, f := range findings {
		parts = append(parts, f.String())
	}
	return strings.Join(parts, "\n  ")
}

// THE LIVE TREE PASSES. This is the guard in its production position: the real registry, the real
// retirement ledger, the real overlap ledger. A new coordinator that duplicates a standing engine
// fails here, in the package that registers it, before it can merge.
func TestContainerDuties_LiveRegistryHasOneOwnerPerDuty(t *testing.T) {
	findings := validateContainerDuties(containerSpecList(), declaredDutyOverlaps, retiredCommandTypes)
	require.Empty(t, findings, "the live container registry violates the duty guard:\n  %s", findingsText(findings))
}

// ANTI-VACUITY. A guard that enumerates nothing passes everything, and this one is a pure function
// over a list someone could hand an empty slice. These assert it is looking at the REAL fleet: the
// whole registry declares, a substantial part of it owns a named duty, and the specific engines the
// two historical incidents were about are present with the duties they actually hold.
//
// The counts are FLOORS, not equalities — neither a new container type nor a legitimate retirement
// should have to edit this test — but they stay high enough that a registry gutted to a handful of
// entries fails. They sit below today's 33/11 for headroom: the sp-fbwqv retirement removed two
// duty-owning types, and a floor pinned at the current value turns every future retirement into a
// false failure. The by-name pins below are the control that does not drift with the count.
func TestContainerDuties_GuardSeesTheRealRegistry(t *testing.T) {
	specs := containerSpecList()
	require.GreaterOrEqual(t, len(specs), 25, "the registry should hold the whole fleet's container types")

	byType := map[string]Duty{}
	named := 0
	for _, spec := range specs {
		byType[spec.CommandType] = spec.Duty
		if spec.Duty != DutyNone && spec.Duty != DutyUnspecified {
			named++
		}
	}
	require.GreaterOrEqual(t, named, 8,
		"only %d types own a named duty — a registry where almost everything is DutyNone cannot catch a duplicate", named)

	// The surviving engines the two historical incidents were about, with the duty each really
	// holds. If a refactor renames or re-scopes one of these, this test is where the retro-validation
	// below stops meaning what it says. (scout_post_coordinator was the fourth until sp-fbwqv
	// deleted it; TestContainerDuties_ScoutPostRetirementIsComplete pins its absence instead.)
	for commandType, want := range map[string]Duty{
		"fleet_growth":              DutyTradeFleetSizing,
		"contract_scaler":           DutyContractFleetSizing,
		"probe_sensing_coordinator": DutyMarketFreshness,
	} {
		require.Equal(t, want, byType[commandType], "%s must own %q for the duty guard to mean anything", commandType, want)
	}
}

// R1: a forgotten Duty field is the hole every other rule falls through — a successor that
// declares nothing collides with nothing. The zero value is UNSPECIFIED for exactly this reason
// (the TuneApplies pattern), and it fails here rather than defaulting to a confident answer.
func TestContainerDuties_UndeclaredDutyFails(t *testing.T) {
	findings := validateContainerDuties([]ContainerSpec{
		{CommandType: "brand_new_coordinator"},
	}, nil, nil)

	require.Len(t, findings, 1)
	require.Contains(t, findings[0].String(), "brand_new_coordinator")
	require.Contains(t, findings[0].String(), "declares no duty")
}

// RETRO-VALIDATION 1 of 2 — THE FLEET AUTOSIZER.
//
// The registry as it actually stood at 1a5a4bbd (2026-07-21), when the dedicated contract scaler
// was registered while the autosizer's HullClassContractDelivery arm was still live and armed
// (contract_delivery_hulls_enabled: true in config.staging.yaml). Two container types, both
// registered, both sizing the contract fleet against one treasury. The autosizer's contract arm
// was not removed until f861cd7c, later the same day, and the type itself survived until
// efe7758c on 2026-08-08.
//
// NOTE ON THE OTHER HALF OF THAT RETIREMENT: the HEAVY handover (7578f332, 2026-08-06) was atomic
// — the same commit that gave fleet_growth its heavy buy path disabled the autosizer's heavy class
// and moved hullbuy.HeavyBuyerContainers to it — so there was never a moment with two declared
// heavy buyers, and the guard correctly would NOT have fired on it. The contract pool is where the
// handover slipped, and the guard fires there.
func TestContainerDuties_RetroValidates_TheFleetAutosizerDuplicate(t *testing.T) {
	asOf1a5a4bbd := []ContainerSpec{
		{CommandType: "fleet_autosizer", Duty: DutyContractFleetSizing},
		{CommandType: "contract_scaler", Duty: DutyContractFleetSizing},
		{CommandType: "fleet_growth", Duty: DutyTradeFleetSizing},
		{CommandType: "navigate_ship", Duty: DutyNone},
	}

	findings := validateContainerDuties(asOf1a5a4bbd, declaredDutyOverlaps, nil)

	require.Len(t, findings, 1, "the duplicated contract-fleet sizing must be the one finding")
	require.Equal(t, DutyContractFleetSizing, findings[0].duty)
	require.Equal(t, []string{"contract_scaler", "fleet_autosizer"}, findings[0].types,
		"the failure must NAME BOTH engines — 'a duplicate exists' is not actionable")
}

// RETRO-VALIDATION 2 of 2 — THE SCOUT-POST COORDINATOR.
//
// The same vocabulary, one level away in the fleet: the scout-post coordinator manned posts whose
// tours scanned MARKETPLACE waypoints, and the parked-sensing coordinator parks probes that scan
// markets forever. Two standing engines, one duty — market freshness.
//
// The registry as it stood at efe7758c, when both were registered. sp-fbwqv has since deleted the
// scout-post coordinator, so this is HISTORICAL and must be fed the historical registry: reading
// the live one would leave the case unrepresented, and a retro-validation that passes because its
// subject vanished asserts nothing at all.
//
// TestContainerDuties_LiveRegistryHasOneOwnerPerDuty is what covers the live tree, and it now
// passes with NO declared overlap — the duplicate is gone rather than excused.
func TestContainerDuties_RetroValidates_TheScoutPostDuplicate(t *testing.T) {
	asOfEfe7758c := []ContainerSpec{
		{CommandType: "scout_post_coordinator", Duty: DutyMarketFreshness},
		{CommandType: "probe_sensing_coordinator", Duty: DutyMarketFreshness},
		{CommandType: "shipyard_backfill_coordinator", Duty: "shipyard-listing coverage"},
		{CommandType: "scout_tour", Duty: DutyNone},
	}

	findings := validateContainerDuties(asOfEfe7758c, declaredDutyOverlaps, nil)

	require.Len(t, findings, 1, "the duplicated market freshness must be the one finding")
	require.Equal(t, DutyMarketFreshness, findings[0].duty)
	require.Equal(t, []string{"probe_sensing_coordinator", "scout_post_coordinator"}, findings[0].types,
		"the failure must NAME BOTH engines")
}

// THE RETIREMENT ACTUALLY HAPPENED. The pair above is history only because sp-fbwqv deleted the
// scout-post coordinator — so pin that, or the test above degrades into a fixture asserting
// something about a world nobody checked is gone. Both halves: the type is retired, and the duty it
// shared is now singly owned on the LIVE registry.
func TestContainerDuties_ScoutPostRetirementIsComplete(t *testing.T) {
	for _, retired := range []string{"scout_post_coordinator", "shipyard_backfill_coordinator", "scout_reposition"} {
		require.True(t, retiredCommandTypes[retired], "%s must be declared retired", retired)
	}

	owners := []string{}
	for _, spec := range containerSpecList() {
		if spec.Duty == DutyMarketFreshness {
			owners = append(owners, spec.CommandType)
		}
	}
	require.Equal(t, []string{"probe_sensing_coordinator"}, owners,
		"market freshness must now have exactly ONE owner")
	require.Empty(t, declaredDutyOverlaps,
		"no overlap may remain declared once its duplicate is retired — a stale exemption is dead surface")
}

// R4: the overlap ledger is an exemption, and an exemption with no date and no bead is a permanent
// one. This is the difference between "declared overlap" and "silenced guard".
func TestContainerDuties_UndatedOverlapFails(t *testing.T) {
	specs := []ContainerSpec{
		{CommandType: "engine_a", Duty: DutyMarketFreshness},
		{CommandType: "engine_b", Duty: DutyMarketFreshness},
	}

	for name, overlap := range map[string]dutyOverlap{
		"no date": {Types: []string{"engine_a", "engine_b"}, Retirement: "sp-xxxx"},
		"no bead": {Types: []string{"engine_a", "engine_b"}, Since: "2026-08-08"},
	} {
		t.Run(name, func(t *testing.T) {
			findings := validateContainerDuties(specs, map[Duty]dutyOverlap{DutyMarketFreshness: overlap}, nil)
			require.Len(t, findings, 1)
			require.Contains(t, findings[0].String(), "unattributed exemption")
		})
	}
}

// R5a: the ledger must not outlive the overlap. A declared overlap whose predecessor HAS been
// retired is itself dead surface — the exact failure mode of this bead's instances 3 and 4, one
// layer up — so resolving an overlap and leaving its entry behind fails.
func TestContainerDuties_StaleOverlapDeclarationFails(t *testing.T) {
	findings := validateContainerDuties(
		[]ContainerSpec{{CommandType: "engine_b", Duty: DutyMarketFreshness}},
		map[Duty]dutyOverlap{DutyMarketFreshness: {
			Types: []string{"engine_a", "engine_b"}, Since: "2026-08-08", Retirement: "sp-fbwqv",
		}}, nil)

	require.Len(t, findings, 1)
	require.Contains(t, findings[0].String(), "the overlap is over")
}

// R5b: an exemption covers the pair it was written for, and nothing else. A THIRD engine joining an
// already-overlapped duty is a new duplicate nobody reviewed, and it must not inherit the first
// pair's dispensation.
func TestContainerDuties_OverlapDoesNotCoverAThirdClaimant(t *testing.T) {
	findings := validateContainerDuties([]ContainerSpec{
		{CommandType: "engine_a", Duty: DutyMarketFreshness},
		{CommandType: "engine_b", Duty: DutyMarketFreshness},
		{CommandType: "engine_c", Duty: DutyMarketFreshness},
	}, map[Duty]dutyOverlap{DutyMarketFreshness: {
		Types: []string{"engine_a", "engine_b"}, Since: "2026-08-08", Retirement: "sp-fbwqv",
	}}, nil)

	require.Len(t, findings, 1)
	require.Equal(t, []string{"engine_a", "engine_b", "engine_c"}, findings[0].types)
	require.Contains(t, findings[0].String(), "does not match its declared overlap")
}

// R3, generalized from TestFleetAutosizer_IsRetiredAndUnbuildable: retiredCommandTypes and
// containerSpecList are the two halves of a retirement, and a type in both is a retirement that
// was announced but not performed — the corpse still has a builder. Asserted for EVERY type, not
// one, so the next retirement inherits the check for free.
func TestContainerDuties_NoRegisteredTypeIsAlsoDeclaredRetired(t *testing.T) {
	for _, spec := range containerSpecList() {
		require.False(t, retiredCommandTypes[spec.CommandType],
			"%s is declared retired but still registered — delete the spec, or the retirement is only a claim", spec.CommandType)
	}

	findings := validateContainerDuties(
		[]ContainerSpec{{CommandType: "fleet_autosizer", Duty: DutyTradeFleetSizing}},
		nil, retiredCommandTypes)
	require.Len(t, findings, 1, "a resurrected retired type must fail the guard")
	require.Contains(t, findings[0].String(), "declared retired AND still registered")
}
