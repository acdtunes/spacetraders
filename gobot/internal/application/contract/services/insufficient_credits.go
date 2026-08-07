package services

import (
	"fmt"
	"strings"
)

// ErrInsufficientCredits signals that a contract cargo purchase failed because the agent's
// treasury could not cover the cost (SpaceTraders API error code 4600). It is PERMANENT relative to
// the current treasury snapshot rather than transient, so the caller must PARK — a clean exit that
// resumes on the coordinator's next tick once credits recover — and never propagate a crash: the
// container runner treats a plain wrapped error as a crash and respawns the worker every few
// seconds, turning one unaffordable buy into a crash loop. It follows the park-not-crash idiom of
// ErrDeferToSupply (manufacturing) and ErrRefuelUnrecoverable (refuel retry).
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
