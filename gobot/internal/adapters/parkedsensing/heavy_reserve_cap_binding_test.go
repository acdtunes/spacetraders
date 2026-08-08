package parkedsensing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// THE SECOND ANSWER, on the state that made it necessary: 4 heavies against an operator cap of 4,
// on a known yard whose ask nobody is reading any more because the pricing errand correctly stopped.
// The reservation is 0 and so is the target price — and the cap verdict is what tells that apart
// from every other zero this port returns.
func TestHeavyReservePort_AtTheCapReportsTheCapAsBinding(t *testing.T) {
	p, _, ctx := portWith(
		&fakeCapSource{exists: true, present: true, cap: 4},
		&fakeCensus{owned: 4},
		&fakeYardPricer{found: false, capabilityOpenUnpriced: true},
	)

	target, capBinding, err := p.Reserve(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, common.HeavyReserveTarget(0), target, "at the cap nothing is saved toward — unchanged")
	require.True(t, capBinding, "an unpriced yard must not hide the cap: at the cap the pricing errand stops reading asks, so a priced target is exactly what is NOT available here")
}

// EVERY OTHER ZERO IS NOT A CAP, and this is the list. Each of these already returned a reserve of
// 0; reporting any of them as a bound cap would attribute the fleet's stillness to an operator dial
// nobody touched — the borrowed-cause defect, moved one layer down.
func TestHeavyReservePort_OtherZeroReservesAreNotTheCapBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caps   *fakeCapSource
		census *fakeCensus
		yards  *fakeYardPricer
	}{
		{
			"no heavy buyer container exists at all",
			&fakeCapSource{exists: false},
			&fakeCensus{owned: 9},
			&fakeYardPricer{price: 1_565_500, found: true},
		},
		{
			"under the cap with a priced yard — the ordinary saving state",
			&fakeCapSource{exists: true, present: true, cap: 5},
			&fakeCensus{owned: 2},
			&fakeYardPricer{price: 1_565_500, found: true},
		},
		{
			"no known yard sells a heavy: availability bars it, not the cap",
			&fakeCapSource{exists: true, present: true, cap: 4},
			&fakeCensus{owned: 4},
			&fakeYardPricer{found: false},
		},
		{
			"an operator hold of 0 is its own decision, not a cap that was met",
			&fakeCapSource{exists: true, present: true, cap: 0},
			&fakeCensus{owned: 0},
			&fakeYardPricer{price: 1_565_500, found: true},
		},
		{
			"a blind census: we could not count the fleet, so we know nothing about a bound",
			&fakeCapSource{exists: true, present: true, cap: 4},
			&fakeCensus{owned: 4, err: errors.New("ships table unreadable")},
			&fakeYardPricer{price: 1_565_500, found: true},
		},
		{
			"a blind yard read: the capability itself is unknown",
			&fakeCapSource{exists: true, present: true, cap: 4},
			&fakeCensus{owned: 4},
			&fakeYardPricer{found: true, price: 1_565_500, err: errors.New("shipyard_inventory unreadable")},
		},
		{
			"an abandoned tick — the work was called off, so nothing was measured",
			&fakeCapSource{err: context.Canceled},
			&fakeCensus{owned: 4},
			&fakeYardPricer{price: 1_565_500, found: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, ctx := portWith(tc.caps, tc.census, tc.yards)

			_, capBinding, err := p.Reserve(ctx, 1)
			require.NoError(t, err)
			require.False(t, capBinding, "%s: this is not the operator's cap, and must not be published as one", tc.name)
		})
	}
}

// AN UNREADABLE CAP FALLS BACK TO THE DOCUMENTED DEFAULT — the existing rung, unchanged — and the
// cap verdict must follow that same resolved number rather than a second opinion about it. Owning
// more heavies than the default means the fallback cap IS met, and saying otherwise would leave the
// two halves of one port describing different bounds.
func TestHeavyReservePort_TheCapVerdictFollowsTheResolvedCap(t *testing.T) {
	p, log, ctx := portWith(
		&fakeCapSource{err: errors.New("containers table unreadable")},
		&fakeCensus{owned: 99},
		&fakeYardPricer{price: 1_565_500, found: true},
	)

	target, capBinding, err := p.Reserve(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, common.HeavyReserveTarget(0), target)
	require.True(t, capBinding, "99 owned against the documented default cap is over it; the verdict must read the cap the reservation actually used")
	require.NotEmpty(t, log.lines, "falling back to the default cap stays LOUD — this test must not be what silences it")
}

// THE TWO ANSWERS COME OFF ONE SET OF READS, and the call counts are what make that falsifiable
// rather than asserted. A second census inside the same tick is how the reservation and the cap
// verdict would come to describe different fleets — the exact divergence this port's whole design,
// and the shared predicate beneath it, exists to prevent.
func TestHeavyReservePort_BothAnswersCostOneReadEach(t *testing.T) {
	census := &fakeCensus{owned: 4}
	yards := &fakeYardPricer{found: false, capabilityOpenUnpriced: true}
	p, _, ctx := portWith(&fakeCapSource{exists: true, present: true, cap: 4}, census, yards)

	_, capBinding, err := p.Reserve(ctx, 1)
	require.NoError(t, err)
	require.True(t, capBinding)
	require.Equal(t, 1, census.calls, "the census must be read exactly once and serve BOTH answers")
	require.Equal(t, 1, yards.calls, "the yard target must be read exactly once and serve BOTH answers")
}
