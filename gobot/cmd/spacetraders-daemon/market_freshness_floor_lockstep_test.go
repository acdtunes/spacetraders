package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	tradeRouteCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// The tour has ONE market-data freshness floor, and it is written down twice.
//
// The config side is the BOOT floor the daemon injects into the firm-sink money guard;
// the commands side is the floor under the listing paths AND the documented default the
// tune registry reports. They are separate consts in packages that cannot import each
// other, held equal only by a comment — which is exactly the arrangement that lets two
// numbers drift apart while every doc string still claims there is one.
//
// The drift is not cosmetic. Because a floor bites only ABOVE the rotation bound, a
// listing floor raised past the sink floor makes the tour PLAN on rows the buy gate then
// refuses — tours built to fail closed — and the reverse leaves the money guard trusting
// rows the planner already threw away. And `tune --show` reports the commands-side number
// as THE default for both, so a split makes the operator-facing default a lie about the
// guard it is supposed to describe.
//
// This is the only place in the tree that can see both, so it is where they are pinned.
func TestMarketDataFreshnessFloor_BootAndTuneDefaultsAreOneNumber(t *testing.T) {
	bootFloor := config.TradeFleetConfig{}.ResolvedSinkFreshnessMaxAge()
	tuneDefault := time.Duration(
		tradeRouteCmd.TradeFleetTunableDefaults()[tradeRouteCmd.TuneKeyMarketDataMaxAgeMinutes],
	) * time.Minute

	assert.Equal(t, bootFloor, tuneDefault,
		"the money guard's boot floor and the operator-facing default must be the same number")
}
