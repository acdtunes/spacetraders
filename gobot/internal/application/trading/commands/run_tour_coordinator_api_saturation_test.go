package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeAPISaturationReader struct {
	permille int
	reads    int
}

func (r *fakeAPISaturationReader) SaturationPermille(_ context.Context) int {
	r.reads++
	return r.permille
}

// A dropped scalar is silent: the solver just ranks on credits per hour and the plan looks
// normal. So the forwarding needs its own pin.
func TestTourAPISaturation_ForwardsTheMeasuredPressure(t *testing.T) {
	reader := &fakeAPISaturationReader{permille: 1000}
	h := &RunTourCoordinatorHandler{apiSaturation: reader}

	require.Equal(t, 1000, h.tourAPISaturation(context.Background()))
	require.Equal(t, 1, reader.reads)
}

// THE FAIL-OPEN PIN, both directions: an unwired estimator and one with no opinion both
// leave 0 on the request, which serializes to nothing.
func TestTourAPISaturation_IsZeroWhenNothingHasBeenMeasured(t *testing.T) {
	unwired := &RunTourCoordinatorHandler{}
	require.Zero(t, unwired.tourAPISaturation(context.Background()))

	silent := &RunTourCoordinatorHandler{apiSaturation: &fakeAPISaturationReader{permille: 0}}
	require.Zero(t, silent.tourAPISaturation(context.Background()))
}

// A negative reading is not a fraction of a ceiling, and the objective must never see it.
func TestTourAPISaturation_RefusesANonPositiveReading(t *testing.T) {
	h := &RunTourCoordinatorHandler{apiSaturation: &fakeAPISaturationReader{permille: -250}}
	require.Zero(t, h.tourAPISaturation(context.Background()))
}

// The setter is what the daemon boot calls; without it the field stays nil.
func TestSetAPISaturationReader_WiresTheEstimator(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	require.Zero(t, h.tourAPISaturation(context.Background()))

	h.SetAPISaturationReader(&fakeAPISaturationReader{permille: 640})
	require.Equal(t, 640, h.tourAPISaturation(context.Background()))
}
