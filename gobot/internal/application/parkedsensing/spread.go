package parkedsensing

import "sort"

// spread.go is the measurement half of the scan rotation: how a scan's prices
// become one number, and how those numbers become the yardstick every slot's
// weight is measured against. Both functions are pure — the scanner decides WHEN
// to measure, this decides what the measurement says.

// GoodPrice is one good's two-sided quote at a waypoint, as the scan just wrote
// it to market_data.
//
// WIRING THIS TYPE IS THE ONE PLACE THE SENSING MODEL CAN BREAK SILENTLY. The
// fields are named from the MARKET's side, and the mapping from the persisted
// columns is deliberately NOT the one field-name matching suggests:
//
//	Bid ← market_data.sell_price      (what the market PAYS us — its bid)
//	Ask ← market_data.purchase_price  (what the market CHARGES us — its ask)
//
// The names are renamed, not swapped: the persisted columns are named from OUR
// side of the trade and these from the market's. Wire them by name and every quote
// comes back inverted — every good skipped by RelativeSpread's guard, every market
// observing a spread of zero, the fleet median collapsing to its 1.0 fallback, and
// every slot on the same optimistic prior. The rotation still runs and reports no
// error; it simply stops preferring the markets worth watching. That silence is
// why the inversion is guarded and logged, and why an inverted quote indicts the
// stored row at least as often as the wiring.
type GoodPrice struct {
	Good string
	// Bid is what the market pays for the good.
	Bid int
	// Ask is what the market charges for it. A quote with Ask below Bid is
	// impossible in a real market and is treated as bad data, not a low reading.
	Ask int
}

// RelativeSpread is the mean relative bid-ask gap across the goods a slot exists
// to watch: (ask-bid)/mid per good, averaged. It also reports how many goods were
// rejected as impossible, which is the caller's cue that something is miswired.
//
// Relative rather than absolute, so a cheap good's gap is comparable with an
// expensive one's — the number feeds a weighting that ranks waypoints against each
// other, and an absolute gap would rank them by price level. Goods the market does
// not quote, or quotes at a non-positive price, are skipped rather than averaged
// in as zeros, so an absent good does not dilute the goods that ARE quoted.
//
// Zero qualifying goods returns a spread of zero, and that IS the intended
// observation: a market that has stopped quoting anything we watch should decay
// out of the hot rotation rather than hold the weight its history earned. Callers
// distinguish it from "nothing was asked" (an empty whitelist) before folding it in.
func RelativeSpread(prices []GoodPrice, whitelist []string) (spread float64, inverted int) {
	if len(whitelist) == 0 || len(prices) == 0 {
		return 0, 0
	}

	watched := make(map[string]struct{}, len(whitelist))
	for _, good := range whitelist {
		watched[good] = struct{}{}
	}

	var sum float64
	var n int
	for _, p := range prices {
		if _, ok := watched[p.Good]; !ok {
			continue
		}
		bid, ask := float64(p.Bid), float64(p.Ask)
		if bid <= 0 || ask <= 0 {
			// A non-positive side is an unquoted good, not a free one. Note that
			// mid is positive whenever both sides are, so it needs no guard.
			continue
		}
		if ask < bid {
			// No real market sells below what it buys at. Counting this as a
			// negative spread would make a miswired adapter (see GoodPrice) look
			// like a merely uninteresting market.
			inverted++
			continue
		}
		sum += (ask - bid) / ((bid + ask) / 2)
		n++
	}

	if n == 0 {
		return 0, inverted
	}
	return sum / float64(n), inverted
}

// fleetMedianSpread is the fleet's typical observed spread, which is what every
// slot's weight is measured against.
//
// Only measured slots count: an unmeasured slot carries a zero it never earned,
// and letting those zeros drag the median down would inflate every measured slot's
// weight against a median that describes nothing. With nothing measured the median
// is 1.0 — not a real spread, but the value that makes ScanWeight hand every slot
// the optimistic prior, since no waypoint has yet earned preference.
func fleetMedianSpread(slots []SensingSlotView) float64 {
	measured := make([]float64, 0, len(slots))
	for _, v := range slots {
		if v.SpreadEWMA > 0 {
			measured = append(measured, v.SpreadEWMA)
		}
	}
	if len(measured) == 0 {
		return 1.0
	}
	sort.Float64s(measured)

	mid := len(measured) / 2
	if len(measured)%2 == 1 {
		return measured[mid]
	}
	return (measured[mid-1] + measured[mid]) / 2
}
