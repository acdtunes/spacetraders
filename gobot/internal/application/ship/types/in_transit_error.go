package types

import (
	"fmt"
	"strings"
	"time"
)

// ErrShipInTransit reports that a ship action (navigate/dock/orbit) was
// rejected by the live API with 4214 because the hull is mid-transit on the
// SERVER while the local snapshot believed it was parked. By the time a
// handler returns this error it has already re-synced the local state from
// the authoritative server nav, so Destination/Arrival describe the REAL
// transit and a caller can wait it out instead of failing its container.
type ErrShipInTransit struct {
	ShipSymbol  string
	Destination string    // server-side transit destination
	Arrival     time.Time // server-side arrival instant (zero when unknown)
	Cause       error     // the original API rejection
}

func (e *ErrShipInTransit) Error() string {
	return fmt.Sprintf("ship %s is in transit to %s (arrives %s): %v",
		e.ShipSymbol, e.Destination, e.Arrival.UTC().Format(time.RFC3339), e.Cause)
}

func (e *ErrShipInTransit) Unwrap() error { return e.Cause }

// IsShipInTransitAPIError reports whether err is the live API's 4214
// rejection ("Ship is currently in-transit ..."). Matched on the wire body
// rather than a typed field because every layer in the call chain wraps the
// client's APIError into plain fmt.Errorf strings.
func IsShipInTransitAPIError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, `"code":4214`) || strings.Contains(msg, "currently in-transit")
}
