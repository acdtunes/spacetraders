package services

import (
	"fmt"
	"strings"
)

// ErrInsufficientCredits signals that a contract cargo purchase failed
// because the agent's treasury could not cover the cost (SpaceTraders API
// error code 4600). This is PERMANENT relative to the current treasury
// snapshot, not transient - the caller must PARK (clean exit, resume on the
// coordinator's next tick once credits recover) rather than propagate a
// crash. This is the third credits-guard surface (sp-vwhi), joining sp-bp6f
// (trade) and sp-9aoc/sp-2dv4 (factory); it follows the park-not-crash idiom
// established by ErrDeferToSupply (sp-hs2j, manufacturing) and
// ErrRefuelUnrecoverable (sp-vsfn, refuel retry).
//
// Before this fix, a 4600 during contract purchasing propagated as a plain
// wrapped error, which the container runner treats as a crash: the
// coordinator respawned the worker roughly every 10s, producing ~18
// container.crashed events in 3 minutes until the captain intervened
// manually (2026-07-XX incident, ELECTRONICS x18 @ 5,566 = 100,188 credits
// needed vs 85,517 available).
type ErrInsufficientCredits struct {
	ShipSymbol     string
	TradeSymbol    string
	UnitsAttempted int

	// CreditsNeeded and CreditsAvailable are 0 until ProcessSingleDelivery
	// enriches them with the purchase cost and a live treasury snapshot.
	// CreditsAvailable is -1 if the live lookup itself failed.
	CreditsNeeded    int
	CreditsAvailable int

	Cause error
}

func (e *ErrInsufficientCredits) Error() string {
	return fmt.Sprintf(
		"insufficient credits to purchase %d units of %s for %s: credits_needed=%d credits_available=%d action=parked reason=insufficient_credits cause=%v",
		e.UnitsAttempted, e.TradeSymbol, e.ShipSymbol, e.CreditsNeeded, e.CreditsAvailable, e.Cause,
	)
}

func (e *ErrInsufficientCredits) Unwrap() error { return e.Cause }

// IsInsufficientCreditsError reports whether err is (or wraps, via %w) a
// SpaceTraders API 4600 "insufficient funds" response. Detection is via
// substring match on the wire-format error text - the API client's error
// type is not exported for errors.As matching, and the "code":4600 substring
// survives every %w-wrapping layer between the API client and this package
// unmodified.
func IsInsufficientCreditsError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), `"code":4600`)
}
