package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNaturalLessOrdersDigitRunsNumerically(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"classic multi-digit trap", "TORWIND-2", "TORWIND-10", true},
		{"reverse of classic trap is false", "TORWIND-10", "TORWIND-2", false},
		{"single digit ordering preserved", "TORWIND-1", "TORWIND-2", true},
		{"equal numeric value with leading zero", "SHIP-02", "SHIP-2", false},
		{"non-numeric prefix falls back to byte compare", "ALPHA-1", "BETA-1", true},
		{"identical strings are not less", "TORWIND-2", "TORWIND-2", false},
		{"shorter prefix sorts first when otherwise equal", "TORWIND", "TORWIND-1", true},
		{"multiple digit runs compare left to right", "X1-A2", "X1-A10", true},
		{"large gap still numeric", "TORWIND-9", "TORWIND-100", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, naturalLess(tc.a, tc.b))
		})
	}
}

func TestSortShipListRowsNaturalOrdersBySymbol(t *testing.T) {
	rows := []shipListRow{
		{Symbol: "TORWIND-10"},
		{Symbol: "TORWIND-3"},
		{Symbol: "TORWIND-1"},
		{Symbol: "TORWIND-2"},
	}

	sortShipListRowsNatural(rows)

	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.Symbol
	}
	require.Equal(t, []string{"TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-10"}, got)
}

func TestSortShipListRowsNaturalIsStableForEqualSymbols(t *testing.T) {
	// Two rows can share a symbol only in pathological test data, but the
	// sort must still be deterministic (stable) rather than reordering ties
	// based on sort algorithm internals.
	rows := []shipListRow{
		{Symbol: "TORWIND-1", Role: "first"},
		{Symbol: "TORWIND-1", Role: "second"},
	}

	sortShipListRowsNatural(rows)

	require.Equal(t, "first", rows[0].Role)
	require.Equal(t, "second", rows[1].Role)
}
