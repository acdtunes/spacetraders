package persistence

import "testing"

// The sink scan ranks by the BID and the source scan by the ASK; swapping a column, a
// direction or an exclusion returns a plausible but wrong best market, so pin all three.
func TestMarketPriceSides_PinColumnDirectionAndExclusion(t *testing.T) {
	cases := []struct {
		name string
		side marketPriceSide
		want marketPriceSide
	}{
		{"bid", bidSide, marketPriceSide{priceColumn: "sell_price", priceDir: "DESC", excludeTradeType: "EXPORT"}},
		{"ask", askSide, marketPriceSide{priceColumn: "purchase_price", priceDir: "ASC", excludeTradeType: "IMPORT"}},
	}
	for _, tc := range cases {
		if tc.side != tc.want {
			t.Errorf("%s side = %+v, want %+v", tc.name, tc.side, tc.want)
		}
	}
}
