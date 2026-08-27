package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// The manifest is FLOWN in the order it is returned, one navigate/dock/orbit bundle per
// change of waypoint, so a manifest that interleaves two sources re-docks at a waypoint it
// has already bought at. Grouping costs nothing — the same goods at the same prices — and
// removes the re-dock outright.
func TestBuildLookbackManifest_GroupsItemsBySourceWaypointSoEachIsVisitedOnce(t *testing.T) {
	src := []trading.GoodListing{
		gl("RICH", "HU21-D46", "EXPORT", 40, 100, 40), // spread 500, capped 20000 — ranks 1st
		gl("MID", "HU21-D47", "EXPORT", 40, 100, 40),  // spread 300, capped 12000 — ranks 2nd
		gl("THIN", "HU21-D46", "EXPORT", 40, 100, 40), // spread 200, capped  8000 — ranks 3rd
	}
	dest := []trading.GoodListing{
		gl("RICH", "UQ16-A1", "IMPORT", 600, 999, 40),
		gl("MID", "UQ16-A1", "IMPORT", 400, 999, 40),
		gl("THIN", "UQ16-A1", "IMPORT", 300, 999, 40),
	}

	manifest := buildLookbackManifest(src, dest, 400, 10, lookbackSourcing{})

	if len(manifest) != 3 {
		t.Fatalf("all three floor-clearing lanes must load, got %d: %+v", len(manifest), manifest)
	}
	visited := map[string]bool{}
	var order []string
	for i, item := range manifest {
		if i == 0 || manifest[i-1].SourceWaypoint != item.SourceWaypoint {
			if visited[item.SourceWaypoint] {
				t.Fatalf("source %s is docked twice — the manifest must group by waypoint, got %+v", item.SourceWaypoint, manifest)
			}
			visited[item.SourceWaypoint] = true
			order = append(order, item.SourceWaypoint)
		}
	}
	if len(order) != 2 {
		t.Fatalf("two sources carry these three goods, got visits %v", order)
	}
	if order[0] != "HU21-D46" {
		t.Fatalf("the richest source must be visited first, got %v", order)
	}
}

// The hull is STANDING at one waypoint when the reposition commits, so buying there costs no
// movement bundle at all — the navigate short-circuits and the dock is already held. A source
// that is merely cheaper by less than the visit is worth is a worse buy once the request it
// costs is priced.
func TestBuildLookbackManifest_SourcesAtTheStandingWaypointRatherThanPayAVisitForAThinnerSaving(t *testing.T) {
	src := []trading.GoodListing{
		gl("PARTS", "HU21-STAND", "EXPORT", 40, 100, 40), // where the hull already is
		gl("PARTS", "HU21-FAR", "EXPORT", 40, 99, 40),    // one credit cheaper, a whole visit away
	}
	dest := []trading.GoodListing{gl("PARTS", "UQ16-A1", "IMPORT", 600, 999, 40)}

	manifest := buildLookbackManifest(src, dest, 400, 10, lookbackSourcing{
		StandWaypoint: "HU21-STAND", VisitCharge: 1000,
	})

	if len(manifest) != 1 {
		t.Fatalf("one good, one item expected, got %+v", manifest)
	}
	if manifest[0].SourceWaypoint != "HU21-STAND" {
		t.Fatalf("a 40-credit total saving cannot buy a 1000-credit visit — source must stay at the standing waypoint, got %+v", manifest[0])
	}
}

// The tail cut. A second source whose whole contribution is worth less than the movement
// bundle it costs is dropped: the manifest loads less and spends fewer requests doing it.
func TestBuildLookbackManifest_DropsASourceWaypointThatCannotEarnItsVisitCharge(t *testing.T) {
	src := []trading.GoodListing{
		gl("RICH", "HU21-D46", "EXPORT", 40, 100, 40), // spread 500 x 40 = 20,000
		gl("THIN", "HU21-D47", "EXPORT", 40, 100, 10), // spread  50 x 10 =    500
	}
	dest := []trading.GoodListing{
		gl("RICH", "UQ16-A1", "IMPORT", 600, 999, 40),
		gl("THIN", "UQ16-A2", "IMPORT", 150, 999, 10),
	}

	manifest := buildLookbackManifest(src, dest, 400, 10, lookbackSourcing{VisitCharge: 5000})

	for _, item := range manifest {
		if item.Good == "THIN" {
			t.Fatalf("a source adding 500 credits must not buy a 5000-credit visit, got %+v", manifest)
		}
	}
	if len(manifest) != 1 || manifest[0].Good != "RICH" {
		t.Fatalf("the source that clears its own visit charge must still load, got %+v", manifest)
	}
}

// The counterfactual: the charge prices a visit, it does not cap the manifest at one source.
// A second source that earns more than its visit costs is still bought.
func TestBuildLookbackManifest_KeepsASourceWaypointThatEarnsItsVisitCharge(t *testing.T) {
	src := []trading.GoodListing{
		gl("RICH", "HU21-D46", "EXPORT", 40, 100, 40),
		gl("ALSO", "HU21-D47", "EXPORT", 40, 100, 40), // spread 300 x 40 = 12,000 > 5,000
	}
	dest := []trading.GoodListing{
		gl("RICH", "UQ16-A1", "IMPORT", 600, 999, 40),
		gl("ALSO", "UQ16-A2", "IMPORT", 400, 999, 40),
	}

	manifest := buildLookbackManifest(src, dest, 400, 10, lookbackSourcing{VisitCharge: 5000})

	if len(manifest) != 2 {
		t.Fatalf("a source earning 12,000 must buy its 5,000-credit visit, got %+v", manifest)
	}
}

// THE DEGRADE-SAFELY GATE. Every path that cannot price a request — no saturation reader, a
// thin window, genuine headroom, a disarming operator — lands on a zero charge, and a zero
// charge must source EXACTLY as the engine did before this change: every waypoint that adds
// value admitted, each good at its cheapest ask, the same units at the same prices. Only the
// visit ORDER (grouped) may differ, and the manifest below has one item per waypoint so even
// that is pinned here.
func TestBuildLookbackManifest_ZeroVisitChargeSourcesEveryWaypointExactlyAsBefore(t *testing.T) {
	src := []trading.GoodListing{
		gl("RICH", "HU21-D46", "EXPORT", 40, 100, 40),
		gl("THIN", "HU21-D47", "EXPORT", 40, 100, 10),
		gl("RICH", "HU21-D48", "EXPORT", 40, 90, 40), // the cheapest RICH ask, a third waypoint
	}
	dest := []trading.GoodListing{
		gl("RICH", "UQ16-A1", "IMPORT", 600, 999, 40),
		gl("THIN", "UQ16-A2", "IMPORT", 150, 999, 10),
	}

	manifest := buildLookbackManifest(src, dest, 400, 10, lookbackSourcing{})

	byGood := map[string]lookbackItem{}
	for _, item := range manifest {
		byGood[item.Good] = item
	}
	if len(manifest) != 2 {
		t.Fatalf("an unpriced visit admits every source that adds value, got %+v", manifest)
	}
	if got := byGood["RICH"]; got.SourceWaypoint != "HU21-D48" || got.SourceAsk != 90 || got.Units != 40 {
		t.Fatalf("RICH must still source at its cheapest ask, got %+v", got)
	}
	if got := byGood["THIN"]; got.SourceWaypoint != "HU21-D47" || got.Units != 10 {
		t.Fatalf("THIN must still load from its own source, got %+v", got)
	}
}

// A standing waypoint that sells nothing the destination imports is not a source at all; it
// must never be admitted as an empty stop, and the rest of the manifest is unaffected.
func TestBuildLookbackManifest_AStandingWaypointWithNoLaneIsNotAStop(t *testing.T) {
	src := []trading.GoodListing{
		gl("NOSINK", "HU21-STAND", "EXPORT", 40, 100, 40), // nothing at the destination imports it
		gl("RICH", "HU21-D46", "EXPORT", 40, 100, 40),
	}
	dest := []trading.GoodListing{gl("RICH", "UQ16-A1", "IMPORT", 600, 999, 40)}

	manifest := buildLookbackManifest(src, dest, 400, 10, lookbackSourcing{
		StandWaypoint: "HU21-STAND", VisitCharge: 5000,
	})

	if len(manifest) != 1 || manifest[0].SourceWaypoint != "HU21-D46" {
		t.Fatalf("the standing waypoint carries no lane — the real source must still load, got %+v", manifest)
	}
}

// The free half of co-location, which holds at EVERY saturation: two markets quoting a good at
// the same ask are not equally good buys, because one of them is where the hull already
// stands. Charging nothing for a request cannot make the second visit worth making.
func TestBuildLookbackManifest_AnEqualAskIsBoughtWhereTheHullAlreadyStands(t *testing.T) {
	src := []trading.GoodListing{
		gl("PARTS", "HU21-AAA", "EXPORT", 40, 100, 40), // sorts first, and is a whole visit away
		gl("PARTS", "HU21-STAND", "EXPORT", 40, 100, 40),
	}
	dest := []trading.GoodListing{gl("PARTS", "UQ16-A1", "IMPORT", 600, 999, 40)}

	manifest := buildLookbackManifest(src, dest, 400, 10, lookbackSourcing{StandWaypoint: "HU21-STAND"})

	if len(manifest) != 1 || manifest[0].SourceWaypoint != "HU21-STAND" {
		t.Fatalf("an equal ask must be bought at the standing waypoint, got %+v", manifest)
	}
	if manifest[0].SourceAsk != 100 || manifest[0].Units != 40 {
		t.Fatalf("co-locating an equal ask must not change what is bought, got %+v", manifest[0])
	}
}

// --- the visit charge itself: resolution and saturation scaling ---

// The charge is ACTIVE with no config present (RULINGS #22) and stays tunable (RULINGS #5).
func TestLookbackVisitCharge_ResolvesTheArmedDefaultAndHonoursTheKnob(t *testing.T) {
	if got := lookbackVisitCharge(&RunTourCoordinatorCommand{}, 1000); got != lookbackSourceCallCreditsDefault {
		t.Fatalf("a fully saturated fleet with no knob must charge the armed default, got %d", got)
	}
	if got := lookbackVisitCharge(&RunTourCoordinatorCommand{LookbackSourceCallCredits: 60_000}, 1000); got != 60_000 {
		t.Fatalf("an explicit lookback_source_call_credits must win, got %d", got)
	}
}

// THE DEGRADE-SAFELY GATE, second half. The charge is the SAME resource sp-c6rx2 prices and
// it fails open on the same reading: no estimator, a thin window, or a fleet with genuine
// request headroom all report saturation 0, and at 0 the charge is 0 — every source waypoint
// that adds value is bought, exactly as before this change. A negative or over-range reading
// can never widen it.
func TestLookbackVisitCharge_DegradesToZeroOnAnUnreadableOrSlackSaturation(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{}
	for _, permille := range []int{0, -1, -1000} {
		if got := lookbackVisitCharge(cmd, permille); got != 0 {
			t.Fatalf("saturation %d must price no visit at all, got %d", permille, got)
		}
	}
	half := lookbackVisitCharge(cmd, 500)
	full := lookbackVisitCharge(cmd, 1000)
	if half != lookbackSourceCallCreditsDefault/2 {
		t.Fatalf("a half-bound budget must charge half the visit, got %d", half)
	}
	if over := lookbackVisitCharge(cmd, 5000); over != full {
		t.Fatalf("a reading past the ceiling must clamp to the full charge, got %d want %d", over, full)
	}
	if disarmed := lookbackVisitCharge(&RunTourCoordinatorCommand{LookbackSourceCallCredits: -1}, 1000); disarmed != 0 {
		t.Fatalf("a negative knob is the operator disarm, got %d", disarmed)
	}
}

// --- integration: the flown manifest docks each source once ---

// lookbackTwoSourceFixture stands the hull at X1-HU21-A, which EXPORTS both PARTS and
// PLATING; X1-HU21-B exports PLATING one credit cheaper. UQ16-B imports both.
func lookbackTwoSourceFixture() *tourFixture {
	fx := lookbackFixture()
	fx.ask["X1-HU21-A"]["PLATING"] = 100
	fx.tv["X1-HU21-A"]["PLATING"] = 40
	fx.ask["X1-HU21-B"]["PLATING"] = 99
	fx.tv["X1-HU21-B"]["PLATING"] = 40
	fx.bid["X1-UQ16-B"]["PLATING"] = 300
	fx.ask["X1-UQ16-B"]["PLATING"] = 300
	fx.tv["X1-UQ16-B"]["PLATING"] = 40
	fx.tradeType["X1-UQ16-B"]["PLATING"] = "IMPORT"
	return fx
}

// The whole point of the change, seen at the effect point: a hull standing on a waypoint that
// can supply the manifest buys BOTH goods there rather than paying a second movement bundle
// to save a credit a unit. The flown timeline shows two buys and no second navigate.
func TestLoadLookbackManifest_BuysBothGoodsAtTheWaypointTheHullIsAlreadyStandingOn(t *testing.T) {
	fx := lookbackTwoSourceFixture()
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	h.SetAPISaturationReader(&fakeAPISaturationReader{permille: 1000})
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-LB-COLO", PlayerID: 1, ContainerID: "ctr-lb-colo"}
	resp := &RunTourCoordinatorResponse{}

	loaded := h.loadLookbackManifest(context.Background(), cmd, resp, map[string]int{}, "X1-HU21", "X1-UQ16", 10_000_000, 0)

	if loaded == 0 {
		t.Fatalf("the manifest must load, got %d units", loaded)
	}
	fx.mu.Lock()
	timeline := strings.Join(fx.timeline, ",")
	navs := append([]string(nil), fx.navDests...)
	fx.mu.Unlock()
	if !strings.Contains(timeline, "BUY:PARTS") || !strings.Contains(timeline, "BUY:PLATING") {
		t.Fatalf("both goods must load, timeline=%q", timeline)
	}
	for _, dest := range navs {
		if dest == "X1-HU21-B" {
			t.Fatalf("the hull must not pay a second visit for a one-credit ask saving, navs=%v", navs)
		}
	}
}
