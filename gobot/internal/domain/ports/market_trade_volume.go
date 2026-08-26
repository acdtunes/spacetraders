package ports

import (
	"encoding/json"
	"strings"
)

// ErrCodeMarketTradeVolume is the API's "Trade good <G> has a limit of N units per
// transaction." rejection: a request asked to move more units in ONE transaction than the
// market takes, so the WHOLE transaction is refused and nothing moves. Its data block
// carries the market's authoritative per-transaction tradeVolume — the mirror of
// ErrCodeCargoShortfall, which corrects a belief about the HULL rather than the MARKET.
const ErrCodeMarketTradeVolume = 4604

// marketTradeVolumeEnvelope is the subset of the error body this helper reads:
//
//	{"error":{"code":4604,"data":{"tradeSymbol":"FOOD","units":330,"tradeVolume":300}}}
//
// TradeVolume is a pointer so an ABSENT field is distinguishable from a real zero: a body
// omitting the limit must never read as "this market takes nothing".
type marketTradeVolumeEnvelope struct {
	Error struct {
		Code int `json:"code"`
		Data struct {
			TradeSymbol string `json:"tradeSymbol"`
			TradeVolume *int   `json:"tradeVolume"`
		} `json:"data"`
	} `json:"error"`
}

// MarketTradeVolumeLimit extracts the market's per-transaction unit limit for good from a
// trade-volume rejection. ok is false for anything short of an unambiguous 4604 naming this
// good with a positive limit — an unreadable body, a different code, a different good — so a
// caller that cannot read the payload keeps the API's verdict rather than guessing at the
// market (fail closed, RULINGS #4). An empty good accepts whichever good the payload names.
func MarketTradeVolumeLimit(err error, good string) (int, bool) {
	var env marketTradeVolumeEnvelope
	body, ok := apiErrorBody(err)
	if !ok {
		return 0, false
	}
	// A Decoder (not Unmarshal) so a body embedded in a longer wrapped message still
	// yields the leading envelope.
	if decodeErr := json.NewDecoder(strings.NewReader(body)).Decode(&env); decodeErr != nil {
		return 0, false
	}
	if env.Error.Code != ErrCodeMarketTradeVolume {
		return 0, false
	}
	if sym := env.Error.Data.TradeSymbol; sym != "" && good != "" && sym != good {
		return 0, false
	}
	limit := env.Error.Data.TradeVolume
	if limit == nil || *limit <= 0 {
		return 0, false
	}
	return *limit, true
}
