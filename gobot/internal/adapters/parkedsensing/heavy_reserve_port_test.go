package parkedsensing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

type fakeCensus struct {
	owned int
	err   error
}

func (f *fakeCensus) CountHeavyHulls(_ context.Context, _ int) (int, error) { return f.owned, f.err }

type fakeYardPricer struct {
	price int
	found bool
	err   error
	types []string
}

func (f *fakeYardPricer) CheapestPricedYard(_ context.Context, _ int, shipTypes []string) (shipyard.ShipTypeAvailability, bool, error) {
	f.types = shipTypes
	return shipyard.ShipTypeAvailability{PurchasePrice: f.price}, f.found, f.err
}

type fakeCapSource struct {
	cap     int
	present bool
	exists  bool
	err     error
}

func (f *fakeCapSource) HeavyCap(_ context.Context, _ int) (value int, present bool, containerExists bool, err error) {
	return f.cap, f.present, f.exists, f.err
}

type warnLogger struct {
	mu    sync.Mutex
	lines []string
}

func (w *warnLogger) Log(level, message string, _ map[string]interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.HasPrefix(strings.ToUpper(level), "WARN") {
		w.lines = append(w.lines, message)
	}
}

func portWith(cap *fakeCapSource, census *fakeCensus, yards *fakeYardPricer) (*HeavyReservePort, *warnLogger, context.Context) {
	log := &warnLogger{}
	return NewHeavyReservePort(census, yards, cap), log, logging.WithLogger(context.Background(), log)
}

// LADDER 1: no autosizer container ⇒ reserve 0. No heavy buyer exists, so there is nothing to save
// for — and probe buying must not be held back for a purchase that can never happen.
func TestHeavyReservePort_NoAutosizerContainerReservesNothing(t *testing.T) {
	p, log, ctx := portWith(
		&fakeCapSource{exists: false},
		&fakeCensus{owned: 0},
		&fakeYardPricer{price: 1_565_500, found: true},
	)
	got, err := p.Reserve(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(0), got, "no autosizer container ⇒ no heavy buyer ⇒ nothing to reserve")
	require.Empty(t, log.lines, "an absent autosizer is an expected configuration, not a fault — it must not warn")
}

// LADDER 2: container exists but heavy_cap is unset ⇒ the COMPILED DEFAULT. Both sides then resolve
// identically, so sensing's reserve can never be computed against a cap the autosizer isn't enforcing.
func TestHeavyReservePort_AbsentCapUsesTheCompiledDefault(t *testing.T) {
	p, _, ctx := portWith(
		&fakeCapSource{exists: true, present: false},
		&fakeCensus{owned: fleetCmd.FleetAutosizerTunableDefaults()["heavy_cap"] - 1},
		&fakeYardPricer{price: 1_565_500, found: true},
	)
	got, err := p.Reserve(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1_565_500), got, "one under the default cap still has room, so a heavy is reserved")

	// At the default cap there is no room and the reserve drops — proving the DEFAULT is what bound it.
	atCap, _, ctx2 := portWith(
		&fakeCapSource{exists: true, present: false},
		&fakeCensus{owned: fleetCmd.FleetAutosizerTunableDefaults()["heavy_cap"]},
		&fakeYardPricer{price: 1_565_500, found: true},
	)
	got2, err := atCap.Reserve(ctx2, 1)
	require.NoError(t, err)
	require.Equal(t, int64(0), got2)
}

// LADDER 3: a present (live-tuned) heavy_cap is what binds — the whole point of reading the config.
func TestHeavyReservePort_PresentCapWins(t *testing.T) {
	// Tuned DOWN to 2 with 2 owned ⇒ at cap ⇒ nothing reserved, even though the default 5 would
	// have left room. A stale default here would hold treasury for a heavy the autosizer refuses.
	p, _, ctx := portWith(
		&fakeCapSource{exists: true, present: true, cap: 2},
		&fakeCensus{owned: 2},
		&fakeYardPricer{price: 1_565_500, found: true},
	)
	got, err := p.Reserve(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(0), got, "the tuned cap must bind, not the compiled default")
}

// LADDER 4: a config READ ERROR ⇒ reserve 0 AND a loud WARNING. Neither direction is money-unsafe,
// so the choice is recoverability: a wrongly-held reserve has no natural end, while a zero reserve
// is inert and recovers the moment the read works. The warning is what stops that state being
// silent — an un-held reserve looks exactly like a healthy fleet that simply never buys a heavy.
func TestHeavyReservePort_ConfigErrorReservesNothingAndWarnsLoudly(t *testing.T) {
	p, log, ctx := portWith(
		&fakeCapSource{err: errors.New("container config unreadable")},
		&fakeCensus{owned: 0},
		&fakeYardPricer{price: 1_565_500, found: true},
	)
	got, err := p.Reserve(ctx, 1)
	require.NoError(t, err, "a config read failure must not halt probe buying")
	require.Equal(t, int64(0), got)
	require.Len(t, log.lines, 1, "an unresolvable heavy_cap MUST be loud — silence here looks identical to a healthy fleet")
	require.Contains(t, log.lines[0], "heavy_cap", "the warning must name the knob that could not be resolved")
}

// AN UNREADABLE CENSUS RESERVES NOTHING AND PROBE BUYING PROCEEDS — the SAME direction the fleet
// autosizer takes (fleet_autosizer_act.go: the reserve is computed only when heaviesOwnedOK, so a
// census error leaves it at 0 and light buying continues). The two consumers used to fail in
// OPPOSITE directions here, and the window is reachable rather than theoretical: the census is
// DB-backed while the autosizer's treasury read is API-backed, so a database-only fault leaves
// treasury readable and the census unreadable — exactly the state where sensing halted entirely
// while the autosizer spent the accumulation on light hulls, unreserved.
//
// It is also what the shared predicate's own rationale asks for: "A reserve that failed 'closed'
// by holding treasury on an unreadable input would starve expansion on a blind signal." Halting
// the spender produced that outcome by a different mechanism.
//
// Nothing here is money-unsafe — the immutable floor and every other guard still bind. The cost is
// strategic (we may buy a probe when we would rather have saved), not solvency.
//
// THE WARNING IS HALF THE FIX. A silent zero is indistinguishable from "nothing to save for", and
// that is how a fleet never saves for a heavy and nobody ever finds out.
//
// The fake is ADVERSARIAL: owned=0 under a cap of 5 with a priced yard would reserve 1,565,500 if
// the error were swallowed, so a swallowed error cannot pass as the required 0.
func TestHeavyReservePort_CensusErrorReservesNothingAndWarns(t *testing.T) {
	p, log, ctx := portWith(
		&fakeCapSource{exists: true, present: true, cap: 5},
		&fakeCensus{owned: 0, err: errors.New("ships table unreadable")},
		&fakeYardPricer{price: 1_565_500, found: true},
	)
	got, err := p.Reserve(ctx, 1)
	require.NoError(t, err, "an unreadable census must not halt probe buying — the autosizer keeps spending on lights either way")
	require.Equal(t, int64(0), got, "a blind reserve is reserve 0, not a held one")
	require.Len(t, log.lines, 1, "reserving nothing because we are BLIND must be visible — a silent zero reads as 'nothing to save for'")
	require.Contains(t, log.lines[0], "census", "the warning must name which read went blind")
	require.Contains(t, log.lines[0], "ships table unreadable", "the warning must carry the underlying error")
}

// The yard read takes the same direction for the same reasons: it is the other half of the same
// DB-backed pair, and the autosizer likewise leaves the reserve at 0 when its own price read
// errors. Leaving this one fail-closed would keep the exact asymmetry the census fix removes,
// reachable whenever shipyard_inventory is unreadable but the ships table is not.
//
// ADVERSARIAL: found=true with a real price, so a swallowed error reserves 1,565,500.
func TestHeavyReservePort_YardErrorReservesNothingAndWarns(t *testing.T) {
	p, log, ctx := portWith(
		&fakeCapSource{exists: true, present: true, cap: 5},
		&fakeCensus{owned: 0},
		&fakeYardPricer{price: 1_565_500, found: true, err: errors.New("shipyard inventory unreadable")},
	)
	got, err := p.Reserve(ctx, 1)
	require.NoError(t, err, "an unreadable yard surface must not halt probe buying")
	require.Equal(t, int64(0), got)
	require.Len(t, log.lines, 1, "a blind yard read must be visible, not a silent zero")
	require.Contains(t, log.lines[0], "yard", "the warning must name which read went blind")
	require.Contains(t, log.lines[0], "shipyard inventory unreadable", "the warning must carry the underlying error")
}

// Capability CLOSED (no known priced heavy yard) ⇒ nothing reserved, and it queries the canonical
// heavy ship-type list rather than a local copy.
func TestHeavyReservePort_NoPricedYardReservesNothing(t *testing.T) {
	yards := &fakeYardPricer{found: false}
	p, _, ctx := portWith(&fakeCapSource{exists: true, present: true, cap: 5}, &fakeCensus{owned: 0}, yards)

	got, err := p.Reserve(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(0), got)
	require.Equal(t, shipyard.DefaultHeavyShipTypes, yards.types, "must query the canonical heavy type list")
}
