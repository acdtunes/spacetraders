package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
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

	require.Equal(t, 1000, h.tourAPISaturation(context.Background(), &RunTourCoordinatorCommand{}))
	require.Equal(t, 1, reader.reads)
}

// THE FAIL-OPEN PIN, both directions: an unwired estimator and one with no opinion both
// leave 0 on the request, which serializes to nothing.
func TestTourAPISaturation_IsZeroWhenNothingHasBeenMeasured(t *testing.T) {
	unwired := &RunTourCoordinatorHandler{}
	require.Zero(t, unwired.tourAPISaturation(context.Background(), &RunTourCoordinatorCommand{}))

	silent := &RunTourCoordinatorHandler{apiSaturation: &fakeAPISaturationReader{permille: 0}}
	require.Zero(t, silent.tourAPISaturation(context.Background(), &RunTourCoordinatorCommand{}))
}

// A negative reading is not a fraction of a ceiling, and the objective must never see it.
func TestTourAPISaturation_RefusesANonPositiveReading(t *testing.T) {
	h := &RunTourCoordinatorHandler{apiSaturation: &fakeAPISaturationReader{permille: -250}}
	require.Zero(t, h.tourAPISaturation(context.Background(), &RunTourCoordinatorCommand{}))
}

// The setter is what the daemon boot calls; without it the field stays nil.
func TestSetAPISaturationReader_WiresTheEstimator(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	require.Zero(t, h.tourAPISaturation(context.Background(), &RunTourCoordinatorCommand{}))

	h.SetAPISaturationReader(&fakeAPISaturationReader{permille: 640})
	require.Equal(t, 640, h.tourAPISaturation(context.Background(), &RunTourCoordinatorCommand{}))
}

// A nil command must not panic the read: the observation is best-effort and the reading it
// publishes is the same one the solver gets (RULINGS #4).
func TestTourAPISaturation_SurvivesANilCommand(t *testing.T) {
	h := &RunTourCoordinatorHandler{apiSaturation: &fakeAPISaturationReader{permille: 640}}
	require.Equal(t, 640, h.tourAPISaturation(context.Background(), nil))
}

// THE VISIBILITY PIN. The scalar multiplies every call-pricing term the solver has, so a 0
// silently disarms all of them — and the plan that comes back looks normal. The DEBUG line
// is how an operator tells an armed fleet from an unarmed one, so it has to carry BOTH the
// reading and whether an estimator was wired at all.
func TestTourAPISaturation_LogsEveryRead(t *testing.T) {
	logs := &metaCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logs)

	h := &RunTourCoordinatorHandler{apiSaturation: &fakeAPISaturationReader{permille: 980}}
	require.Equal(t, 980, h.tourAPISaturation(ctx, &RunTourCoordinatorCommand{PlayerID: 7}))

	entry := logs.findByAction("tour_api_saturation")
	require.NotNil(t, entry)
	require.Equal(t, "DEBUG", entry.level)
	require.Equal(t, 980, entry.metadata["permille"])
	require.Equal(t, true, entry.metadata["wired"])
}

// The 0 readings are the ones worth logging, and the two ways of reaching 0 have to be
// distinguishable: no estimator port at all (wired=false) versus an estimator with nothing
// to report (wired=true, permille 0). Only the first is a wiring bug.
func TestTourAPISaturation_LogSeparatesUnwiredFromSilent(t *testing.T) {
	unwiredLogs := &metaCapturingLogger{}
	unwired := &RunTourCoordinatorHandler{}
	require.Zero(t, unwired.tourAPISaturation(
		common.WithLogger(context.Background(), unwiredLogs), &RunTourCoordinatorCommand{}))
	entry := unwiredLogs.findByAction("tour_api_saturation")
	require.NotNil(t, entry)
	require.Equal(t, 0, entry.metadata["permille"])
	require.Equal(t, false, entry.metadata["wired"])

	silentLogs := &metaCapturingLogger{}
	silent := &RunTourCoordinatorHandler{apiSaturation: &fakeAPISaturationReader{permille: 0}}
	require.Zero(t, silent.tourAPISaturation(
		common.WithLogger(context.Background(), silentLogs), &RunTourCoordinatorCommand{}))
	entry = silentLogs.findByAction("tour_api_saturation")
	require.NotNil(t, entry)
	require.Equal(t, 0, entry.metadata["permille"])
	require.Equal(t, true, entry.metadata["wired"])
}
