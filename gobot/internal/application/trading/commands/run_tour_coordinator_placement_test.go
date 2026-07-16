package commands

// sp-f1yk Deliverable 5 — OR-Tools placement/relocation black-box acceptance tests.
//
// WAVE-1 / W4 — GATED on sp-z7ng (fleet-median placement engine: score(x)=E_x - beta*D_x
// with the park floor phi = 0.3 * fleet-median, sourced from TourTelemetryRepository).
// SCAFFOLD ONLY in W1: these two behavioral scenarios are skip-marked TODO(W4) and land
// once z7ng merges.
//
// SEAM CHECK before removing the skips (defer if it fails):
//
//	grep -nE "median|beta|park|score\(x\)|0\.3" run_tour_coordinator_reposition.go
//
// must show beta*D_x + phi=0.3*median, AND beta/phi sourced from TourTelemetryRepository.
// ALSO confirm z7ng's final entry point: today maybeReposition fires on margins-death
// within the loop (reposition.go ~:148); the spec wants a self-triggering
// SENSE->PLAN->DIFF placement loop. The Handle path these tests drive MUST match z7ng's
// final shape — pin with z7ng (§7 z7ng contract) before wiring.
//
// OWNERSHIP (resolves the layer/duplication finding): z7ng owns the WHITE-BOX unit tests
// of score(x) and the phi park-floor math. f1yk owns ONLY these two BLACK-BOX acceptance
// scenarios through the Handle port, asserting on OUTCOME (reposition committed vs held),
// never on internal score values. Diff against z7ng's run_tour_coordinator_reposition_test.go
// + _offcircuit_test.go to prove non-duplication before writing the live wiring.
//
// W4 WIRING PLAN (both seams already exist on main; z7ng wires the median through them):
//   - E_x           : tourFakeRoutingClient{planFn: func(ship routing.TourShipState)
//                     *routing.TourPlan} returns a per-system ProjectedCreditsPerHour.
//   - fleet-median  : tourFakeTelemetry{rows: []trading.TourLegTelemetry} — ListByPlayer
//     (beta and phi)  returns rows; z7ng reads them to compute the trailing-1h median.
//                     f1yk's tests set BOTH planFn (E_x) AND telemetry rows (beta/phi).
//   - entry         : h := newTourHandler(t, fx, planner, tel); h.Handle(ctx, request);
//                     assert response.Repositions and/or fakeRepositionPersister.recorded().

import "testing"

// RED#12 — TestPlacementRelocatesOnExhaustedPocket:
// planFn returns ~0 cph at the current system (E_s ~= 0) and high cph at a reachable x
// (E_x rich); telemetry rows set so beta*D_x < E_x - E_s. Assert a reposition to x is
// COMMITTED (response.Repositions == 1; persister TargetSystem == x).
func TestPlacementRelocatesOnExhaustedPocket(t *testing.T) {
	t.Skip("TODO(W4): gated on sp-z7ng fleet-median placement engine (not yet on main); " +
		"wire E_x via tourFakeRoutingClient.planFn + beta/phi via tourFakeTelemetry rows, " +
		"drive h.Handle, assert response.Repositions == 1 to the rich pocket.")
}

// RED#13 — TestPlacementParksOnGloballySaturatedFleet:
// planFn returns low cph everywhere; telemetry rows set a high fleet-median so
// phi = 0.3*median sits above every reachable E. Assert HOLD (response.Repositions == 0;
// no reposition persisted).
func TestPlacementParksOnGloballySaturatedFleet(t *testing.T) {
	t.Skip("TODO(W4): gated on sp-z7ng fleet-median park floor (not yet on main); " +
		"set a high fleet-median via tourFakeTelemetry so phi=0.3*median dominates every " +
		"reachable E, drive h.Handle, assert response.Repositions == 0 (park/hold).")
}
