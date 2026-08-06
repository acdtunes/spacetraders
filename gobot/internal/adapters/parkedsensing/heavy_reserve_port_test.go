package parkedsensing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
)

type fakeCensus struct {
	owned int
	err   error
}

func (f *fakeCensus) CountHeavyHulls(_ context.Context, _ int) (int, error) { return f.owned, f.err }

type fakeYardPricer struct {
	price int
	found bool
	// capabilityOpenUnpriced is the production state: a known heavy yard whose ask nobody has
	// ever read, so the capability is open with no price to reserve against.
	capabilityOpenUnpriced bool
	err                    error
}

// HeavyTarget is the SHARED heavy-target read (sp-fwk8z): the yard the purchase path would
// target and its ask, resolved by the very same implementation the fleet autosizer consumes.
//
// THE TARGET COMES BACK ALONGSIDE THE ERROR, mirroring fakeCensus. This fake used to zero its
// answer whenever it failed, and that made the fail-safe below untestable: with a zero target the
// reserve is 0 by ARITHMETIC, so deleting the port's `return 0, nil` after warnBlindReserve left
// the whole suite green while a blind yard read silently reserved whatever the stale target said.
// Handing back a live 1,565,500 target WITH the error is what makes a swallowed error observable —
// it is the same adversarial shape TestHeavyReservePort_YardErrorReservesNothingAndWarns already
// claimed in prose but did not deliver.
func (f *fakeYardPricer) HeavyTarget(_ context.Context, _ int) (shipyardQueries.HeavyTarget, error) {
	if !f.found {
		return shipyardQueries.HeavyTarget{CapabilityOpen: f.capabilityOpenUnpriced}, f.err
	}
	return shipyardQueries.HeavyTarget{CapabilityOpen: true, Priced: true, PurchasePrice: int64(f.price)}, f.err
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

func portWith(caps *fakeCapSource, census *fakeCensus, yards *fakeYardPricer) (*HeavyReservePort, *warnLogger, context.Context) {
	log := &warnLogger{}
	return NewHeavyReservePort(census, yards, caps), log, logging.WithLogger(context.Background(), log)
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
	require.Equal(t, common.HeavyReserveTarget(0), got, "no autosizer container ⇒ no heavy buyer ⇒ nothing to reserve")
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
	require.Equal(t, common.HeavyReserveTarget(1_565_500), got, "one under the default cap still has room, so a heavy is reserved")

	// At the default cap there is no room and the reserve drops — proving the DEFAULT is what bound it.
	atCap, _, ctx2 := portWith(
		&fakeCapSource{exists: true, present: false},
		&fakeCensus{owned: fleetCmd.FleetAutosizerTunableDefaults()["heavy_cap"]},
		&fakeYardPricer{price: 1_565_500, found: true},
	)
	got2, err := atCap.Reserve(ctx2, 1)
	require.NoError(t, err)
	require.Equal(t, common.HeavyReserveTarget(0), got2)
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
	require.Equal(t, common.HeavyReserveTarget(0), got, "the tuned cap must bind, not the compiled default")
}

// LADDER 4: an UNREADABLE heavy_cap resolves to the DOCUMENTED DEFAULT — the same value the
// autosizer itself falls back to — and still warns loudly (sp-fwk8z).
//
// It used to reserve nothing, and that was a live divergence rather than a safe default. The fleet
// autosizer, reading THE SAME KNOB from THE SAME TABLE, explicitly refuses to read a failed read
// as zero: liveHeavyCap falls back to its launch value on a snapshot error, documented as "NEVER
// 0, which would read as an operator hold and silently stop all heavy buying". Sensing doing the
// opposite meant the two consumers of one shared predicate disagreed about the cap in precisely
// the window where they are otherwise guaranteed to agree — and the reservation then silently
// never engaged, which is indistinguishable from the feature being switched off.
//
// It cannot wrongly hold treasury for long: the census and yard reads that follow hit the SAME
// database, so a persistent fault still reserves zero through them. The only window this changes
// is the narrow one where the containers table alone is unreadable, and there the autosizer is
// running on its compiled default too. No money guard is touched — the predicate only withholds.
func TestHeavyReservePort_UnreadableCapFallsBackToTheDocumentedDefaultAndWarns(t *testing.T) {
	// Under the default cap ⇒ the reservation still forms, which is the whole point.
	p, log, ctx := portWith(
		&fakeCapSource{err: errors.New("container config unreadable")},
		&fakeCensus{owned: fleetCmd.FleetAutosizerTunableDefaults()["heavy_cap"] - 1},
		&fakeYardPricer{price: 1_565_500, found: true},
	)
	got, err := p.Reserve(ctx, 1)
	require.NoError(t, err, "a config read failure must not halt probe buying")
	require.Equal(t, common.HeavyReserveTarget(1_565_500), got, "an unreadable cap must not silently stop the fleet saving for a heavy")
	require.Len(t, log.lines, 1, "an unresolvable heavy_cap MUST still be loud — the operator has a container read to investigate")
	require.Contains(t, log.lines[0], "heavy_cap", "the warning must name the knob that could not be resolved")

	// AT the default cap ⇒ nothing reserved, proving the DEFAULT is genuinely what bound it rather
	// than an unbounded fallback that would reserve forever.
	atCap, _, capCtx := portWith(
		&fakeCapSource{err: errors.New("container config unreadable")},
		&fakeCensus{owned: fleetCmd.FleetAutosizerTunableDefaults()["heavy_cap"]},
		&fakeYardPricer{price: 1_565_500, found: true},
	)
	got2, err := atCap.Reserve(capCtx, 1)
	require.NoError(t, err)
	require.Equal(t, common.HeavyReserveTarget(0), got2, "the fallback must be the documented cap, not an unbounded one")
}

// A CANCELLED TICK IS A SHUTDOWN, NOT A FAULT — it reserves nothing and says nothing.
//
// This is the sp-fwk8z false-alarm fix. Every occurrence of the live "could not resolve the
// autosizer's heavy_cap (... context canceled)" warning landed within two seconds of a whole-
// process shutdown, in the same millisecond as every other container's "Context canceled, stopping
// container" — daemon restarts, never a healthy tick. On such a tick nothing works and nothing is
// bought, so a warning there is pure noise.
//
// Noise is expensive in one specific way: this warning is the ONLY signal separating "not saving
// because we are blind" from "not saving because there is nothing to save for", and a line that
// cries wolf once per restart is one operators learn to scroll past. Suppressing the abandoned-work
// case is what keeps the remaining warnings worth reading.
func TestHeavyReservePort_CancelledTickIsSilent(t *testing.T) {
	// THE TWO HALVES ARE EXERCISED SEPARATELY, and that separation is the point. A fixture that
	// cancelled the context AND returned context.Canceled would pass with either half of the
	// abandoned() predicate deleted, so it would pin neither. Each case here is detectable by
	// exactly one half:
	//
	//   - "context done" carries an ORDINARY error, so only the ctx.Err() check can see it. This
	//     is the real shutdown shape: the pool refuses a connection the moment the ctx is done and
	//     the driver's message is whatever it happens to be.
	//   - "cancelled error on a live tick" leaves the context ALIVE, so only the errors.Is check
	//     can see it. This is an inner context carrying the cancellation up — still abandoned
	//     work, still nothing an operator can act on.
	type fixture struct {
		cap    *fakeCapSource
		census *fakeCensus
		yards  *fakeYardPricer
		// cancelTick reports whether the tick's own context is done.
		cancelTick bool
	}
	plainErr := errors.New("driver: bad connection")
	livePricer := func() *fakeYardPricer { return &fakeYardPricer{price: 1_565_500, found: true} }
	liveCap := func() *fakeCapSource { return &fakeCapSource{exists: true, present: true, cap: 5} }

	cases := map[string]fixture{
		"context done, cap read fails with an ordinary error":    {cap: &fakeCapSource{err: plainErr}, census: &fakeCensus{owned: 0}, yards: livePricer(), cancelTick: true},
		"context done, census fails with an ordinary error":      {cap: liveCap(), census: &fakeCensus{err: plainErr}, yards: livePricer(), cancelTick: true},
		"context done, yard read fails with an ordinary error":   {cap: liveCap(), census: &fakeCensus{owned: 0}, yards: &fakeYardPricer{err: plainErr}, cancelTick: true},
		"live tick, cap read returns a cancelled error":          {cap: &fakeCapSource{err: context.Canceled}, census: &fakeCensus{owned: 0}, yards: livePricer()},
		"live tick, census returns a cancelled error":            {cap: liveCap(), census: &fakeCensus{err: context.Canceled}, yards: livePricer()},
		"live tick, yard read returns a deadline-exceeded error": {cap: liveCap(), census: &fakeCensus{owned: 0}, yards: &fakeYardPricer{err: context.DeadlineExceeded}},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			p, log, ctx := portWith(f.cap, f.census, f.yards)
			if f.cancelTick {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}

			got, err := p.Reserve(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, common.HeavyReserveTarget(0), got, "an abandoned tick buys nothing either way, so it reserves nothing")
			require.Empty(t, log.lines, "abandoned work must not raise the one alarm that means the feature is broken")
		})
	}
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
	require.Equal(t, common.HeavyReserveTarget(0), got, "a blind reserve is reserve 0, not a held one")
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
	require.Equal(t, common.HeavyReserveTarget(0), got)
	require.Len(t, log.lines, 1, "a blind yard read must be visible, not a silent zero")
	require.Contains(t, log.lines[0], "yard", "the warning must name which read went blind")
	require.Contains(t, log.lines[0], "shipyard inventory unreadable", "the warning must carry the underlying error")
}

// No target to save toward ⇒ nothing reserved — in BOTH of the states that can produce it.
//
// The two are genuinely different and the port must reserve zero for each. "No heavy yard is known
// at all" is a fact about the map. "A heavy yard IS known but nobody has ever read its ask" is the
// production state (a shipyard prices its hulls only while a ship stands there), and it is the one
// the pricing errand exists to resolve — the capability is OPEN and there is still no number a
// money guard could act on, so holding treasury against it would stall expansion indefinitely.
//
// The canonical heavy ship-type list is no longer this port's to choose: the shared heavy-target
// query owns it, and TestHeavyTarget_UnpricedCatalogueRowOpensTheCapabilityWithoutAPrice pins it
// there. This port's job is to TRANSPORT that answer, which is what these cases hold it to.
func TestHeavyReservePort_NoTargetYardReservesNothing(t *testing.T) {
	cases := map[string]*fakeYardPricer{
		"no heavy yard known at all":       {found: false},
		"known heavy yard, ask never read": {found: false, capabilityOpenUnpriced: true},
	}
	for name, yards := range cases {
		t.Run(name, func(t *testing.T) {
			p, _, ctx := portWith(&fakeCapSource{exists: true, present: true, cap: 5}, &fakeCensus{owned: 0}, yards)

			got, err := p.Reserve(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, common.HeavyReserveTarget(0), got)
		})
	}
}

// SINGLE SLOT, END TO END: expansion gets a spending window between heavy purchases.
//
// This is the asymmetry the design rests on (§3), read from the side that actually pays for it.
// Expansion is a continuous small spender; a heavy is one large purchase that requires
// ACCUMULATION. Reserving cap−owned would hold back five heavies' worth from the first tick and
// stop probe buying until the whole heavy fleet was bought — "protecting" the heavies by starving
// expansion outright. Reserving exactly ONE means probe buying pauses only while the NEXT heavy
// accumulates, then resumes the moment it lands.
//
// The walk is what makes that falsifiable rather than asserted. It steps owned from 0 to the cap
// and holds the reserve to a flat line at exactly one heavy's ask the whole way — a cap−owned
// reservation would start at 5× and taper, and a per-purchase reservation would grow. At the cap
// it must drop to zero, releasing the treasury back to expansion for good.
func TestHeavyReservePort_ReservesOneHeavyAtATimeThenReleasesAtTheCap(t *testing.T) {
	const heavyCap = 5
	const ask = common.HeavyReserveTarget(1_565_500)

	for owned := 0; owned < heavyCap; owned++ {
		p, _, ctx := portWith(
			&fakeCapSource{exists: true, present: true, cap: heavyCap},
			&fakeCensus{owned: owned},
			&fakeYardPricer{price: int(ask), found: true},
		)
		got, err := p.Reserve(ctx, 1)
		require.NoError(t, err)
		require.Equal(t, ask, got,
			"owned=%d (headroom %d): reserve must be exactly ONE heavy's ask, never cap−owned multiples — expansion needs a spending window between purchases",
			owned, heavyCap-owned)
	}

	// At the cap the reservation ends: nothing further is being saved for, and every credit is
	// released back to expansion. Written for at-cap AND over-cap, because a hull acquired outside
	// this path must not leave a reservation standing forever.
	for _, owned := range []int{heavyCap, heavyCap + 3} {
		p, _, ctx := portWith(
			&fakeCapSource{exists: true, present: true, cap: heavyCap},
			&fakeCensus{owned: owned},
			&fakeYardPricer{price: int(ask), found: true},
		)
		got, err := p.Reserve(ctx, 1)
		require.NoError(t, err)
		require.Equal(t, common.HeavyReserveTarget(0), got, "owned=%d at cap %d: the reserve must release the treasury to expansion", owned, heavyCap)
	}
}
