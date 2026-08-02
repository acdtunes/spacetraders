package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// MONEY ENVELOPE (design §3): a buy proceeds ONLY while it stays within the per-haul cap AND
// leaves the 200k contract cushion intact — and fails CLOSED on an unreadable treasury
// (RULINGS #4). The fence reuses the SAME common.ReserveFloorGate idle-arb uses, floored at
// the 200k ContractScalerCushion.
func TestLongHaulEnvelope_PermitsBuy_FailsClosedOnCushionAndPerHaulCap(t *testing.T) {
	cases := []struct {
		name    string
		fence   common.ReserveFloorGate
		perHaul int64
		buyCost int64
		wantOK  bool
	}{
		{"within cap, leaves cushion", newLongHaulFence(2_000_000), 1_000_000, 800_000, true},
		{"exceeds per-haul cap", newLongHaulFence(50_000_000), 1_000_000, 1_200_000, false},
		{"would breach 200k cushion", newLongHaulFence(900_000), 1_000_000, 750_000, false}, // 900k-750k=150k < 200k
		{"treasury unreadable -> fail closed", unreadableLongHaulFence(), 1_000_000, 100_000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLongHaulEnvelope(tc.fence, tc.perHaul)
			ok, reason := e.permitsBuy(tc.buyCost)
			require.Equal(t, tc.wantOK, ok)
			if !ok {
				require.NotEmpty(t, reason, "a refusal always names its reason (never a silent zero)")
			}
		})
	}
}

// spendCeiling sizes a buy to the smaller of the per-haul cap and the treasury headroom above
// the 200k cushion — and to ZERO when the treasury is unreadable or already below the cushion
// (fail-closed).
func TestLongHaulEnvelope_SpendCeiling(t *testing.T) {
	cases := []struct {
		name    string
		fence   common.ReserveFloorGate
		perHaul int64
		want    int64
	}{
		{"capped by per-haul (deep treasury)", newLongHaulFence(50_000_000), 1_000_000, 1_000_000},
		{"capped by cushion headroom", newLongHaulFence(700_000), 1_000_000, 500_000}, // 700k-200k
		{"below cushion -> 0", newLongHaulFence(150_000), 1_000_000, 0},               // 150k-200k<0
		{"unreadable -> 0 (fail-closed)", unreadableLongHaulFence(), 1_000_000, 0},
		{"no fence wired -> per-haul cap", common.ReserveFloorGate{}, 1_000_000, 1_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, newLongHaulEnvelope(tc.fence, tc.perHaul).spendCeiling())
		})
	}
}
