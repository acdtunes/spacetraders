package ports

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrCodeCargoShortfall is the API's "Ship <S> cargo does not contain N unit(s) of
// <G>. Ship has M unit(s)." rejection: a request asked to remove more units of a good
// than the hull actually holds. The payload's data block carries the hull's
// AUTHORITATIVE on-hand count, which is the only trustworthy number in the exchange —
// our own belief is precisely what the rejection disproved.
//
// This lives beside APIError because it is the same concern: how to read the API's
// error envelope. Several subsystems recover from this code (the construction drain's
// phantom-cargo defer, the contract delivery cache reconcile, the sell clamp), so the
// envelope is parsed in exactly one place rather than re-derived per caller.
const ErrCodeCargoShortfall = 4219

// cargoShortfallEnvelope is the subset of the API's error body these helpers read:
//
//	{"error":{"code":4219,"message":"...","data":{
//	   "shipSymbol":"TORWIND-D7","tradeSymbol":"MACHINERY","cargoUnits":11,"unitsToRemove":71}}}
//
// CargoUnits is a pointer so an ABSENT field is distinguishable from a real zero — a
// body that omits the count must never be read as "the hull is empty", which would
// silently turn an unparseable rejection into a phantom-cargo verdict.
type cargoShortfallEnvelope struct {
	Error struct {
		Code int `json:"code"`
		Data struct {
			TradeSymbol string `json:"tradeSymbol"`
			CargoUnits  *int   `json:"cargoUnits"`
		} `json:"data"`
	} `json:"error"`
}

// CargoShortfallUnits extracts the server's authoritative on-hand count of good from a
// cargo-shortfall rejection. ok is false for every error that is not an unambiguous
// shortfall naming this good with a usable count, so a caller that cannot read the
// payload keeps the API's original verdict rather than guessing at the hold — fail
// closed (RULINGS #4).
//
// Refusals, each of which leaves the rejection standing untouched:
//   - no recoverable JSON body, or a body that does not parse;
//   - a code other than 4219 (a different rejection says nothing about the hold);
//   - a data block naming a DIFFERENT good than the one in question — the count is
//     authoritative only for the good the payload names;
//   - an absent or negative cargoUnits.
//
// Pass an empty good to accept whichever good the payload names.
func CargoShortfallUnits(err error, good string) (int, bool) {
	env, ok := parseCargoShortfall(err)
	if !ok {
		return 0, false
	}
	if sym := env.Error.Data.TradeSymbol; sym != "" && good != "" && sym != good {
		return 0, false
	}
	units := env.Error.Data.CargoUnits
	if units == nil || *units < 0 {
		return 0, false
	}
	return *units, true
}

// IsCargoShortfall reports whether err is a cargo-shortfall rejection: the hull does
// not hold the cargo the caller believed it did. Unlike CargoShortfallUnits this does
// not require a usable count, so it suits callers that only need to know the cache was
// wrong (resync and recover) rather than by how much.
//
// It matches on the PARSED error code, not a bare substring of the message, so an
// unrelated error that merely happens to contain the digits (a ship symbol, a credit
// amount, a waypoint) is not mistaken for one.
func IsCargoShortfall(err error) bool {
	_, ok := parseCargoShortfall(err)
	return ok
}

// parseCargoShortfall recovers and decodes the error envelope, reporting ok only for a
// well-formed body carrying the shortfall code.
func parseCargoShortfall(err error) (cargoShortfallEnvelope, bool) {
	var env cargoShortfallEnvelope
	body, ok := apiErrorBody(err)
	if !ok {
		return env, false
	}
	// A Decoder (not Unmarshal) so a body embedded in a longer wrapped message —
	// trailing prose after the JSON object — still yields the leading envelope.
	if decodeErr := json.NewDecoder(strings.NewReader(body)).Decode(&env); decodeErr != nil {
		return env, false
	}
	if env.Error.Code != ErrCodeCargoShortfall {
		return env, false
	}
	return env, true
}

// apiErrorBody recovers the raw JSON response body from an API rejection.
//
// The typed *APIError carries it directly and survives arbitrary %w wrapping (the
// adapter wraps every call — "failed to sell cargo: %w"), so errors.As is the
// production path. The string fallback covers a rejection that reached us as a plain
// fmt.Errorf carrying the same "API error (status N): {body}" wording, which is the
// shape several older classifiers in this codebase already match on.
func apiErrorBody(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Body, true
	}
	msg := err.Error()
	if i := strings.Index(msg, "{"); i >= 0 {
		return msg[i:], true
	}
	return "", false
}
