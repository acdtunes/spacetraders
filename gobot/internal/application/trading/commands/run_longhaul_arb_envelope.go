package commands

// run_longhaul_arb_envelope.go — the aggressive-but-fail-closed money envelope for the
// long-haul engine (sp-mepj §3). Admiral-authorized SIZING (default ~1M/haul, ~2M total
// exposure, both live-tunable); the MECHANISM stays fail-closed and is never weakened
// (RULINGS #4). It REUSES the shared common.ReserveFloorGate for the treasury fence — the
// SAME gate idle-arb uses — parametrized with the 200k ContractScalerCushion floor so
// long-haul capital never dips into contract working capital, and the shared
// common.ImmutableReserveFloor (50k, RULINGS #5) is untouched (200k > 50k).

import (
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

const (
	// defaultLongHaulPerHaulCap is the per-haul working-capital cap (~1M): the most a
	// single out or backhaul buy may spend. Aggressive by Admiral authorization; live-tunable.
	defaultLongHaulPerHaulCap int64 = 1_000_000

	// defaultLongHaulTotalExposureCap is the total in-flight long-haul exposure cap (~2M):
	// the most capital that may sit in unsold long-haul cargo across the fleet at once. It is
	// enforced as maxConcurrentHauls (below): worst-case exposure = concurrent × perHaulCap.
	defaultLongHaulTotalExposureCap int64 = 2_000_000
)

// newLongHaulFence builds the treasury cushion fence for a live-treasury balance: the shared
// gate with Floor = the 200k ContractScalerCushion, so a buy that would drop treasury below
// the contract cushion is refused (RULINGS #4, cushion FENCED).
func newLongHaulFence(treasury int64) common.ReserveFloorGate {
	return common.ReserveFloorGate{Active: true, Treasury: treasury, Floor: common.ContractScalerCushion}
}

// unreadableLongHaulFence is the fail-closed fence for when the live treasury could not be
// read: every buy is refused and the spend ceiling is zero — never a blind spend.
func unreadableLongHaulFence() common.ReserveFloorGate {
	return common.ReserveFloorGate{Active: true, Unreadable: true}
}

// longHaulEnvelope is one buy's money envelope: the per-haul cap plus the shared cushion
// fence. The total-exposure cap (design guard 3) is enforced UPSTREAM as the fleet
// coordinator's concurrent-haul limit (maxConcurrentHauls), so a single worker only enforces
// the per-haul + cushion bounds here — each worker holds at most one perHaulCap tranche at a
// time (it sells the out cargo before buying the backhaul), so concurrent × perHaulCap bounds
// total in-flight exposure.
type longHaulEnvelope struct {
	perHaulCap int64                   // credits; 0 → treated as no explicit per-haul cap
	fence      common.ReserveFloorGate // Floor = ContractScalerCushion (200k), fail-closed
}

// newLongHaulEnvelope wires the envelope from a resolved fence and a per-haul cap (<=0 →
// defaultLongHaulPerHaulCap).
func newLongHaulEnvelope(fence common.ReserveFloorGate, perHaulCap int64) longHaulEnvelope {
	if perHaulCap <= 0 {
		perHaulCap = defaultLongHaulPerHaulCap
	}
	return longHaulEnvelope{perHaulCap: perHaulCap, fence: fence}
}

// spendCeiling is the maximum credits a buy may spend under the envelope: the smaller of the
// per-haul cap and the treasury headroom above the 200k cushion. It is what the worker sizes
// the tranche against (min(cargo, spendCeiling/ask, optimal_q, absorption)). Fail-closed: an
// unreadable treasury yields 0 — never size a buy on a treasury we could not read (RULINGS
// #4). With no fence wired at all, the per-haul cap alone bounds it (the executor's own
// spend-floor backstop still fences the cushion at buy time).
func (e longHaulEnvelope) spendCeiling() int64 {
	if !e.fence.Active {
		return e.perHaulCap
	}
	if e.fence.Unreadable {
		return 0
	}
	headroom := e.fence.Treasury - e.fence.Floor
	if headroom < 0 {
		headroom = 0
	}
	if headroom < e.perHaulCap {
		return headroom
	}
	return e.perHaulCap
}

// permitsBuy reports whether a buyCost-credit buy clears the envelope — within the per-haul
// cap AND leaving the 200k contract cushion intact — returning (false, reason) on a refusal.
// Fail-closed on an unreadable treasury (the fence Holds everything). Every refusal is an
// explicit reason, never a silent zero.
func (e longHaulEnvelope) permitsBuy(buyCost int64) (bool, string) {
	if buyCost > e.perHaulCap {
		return false, fmt.Sprintf("buy %d exceeds the per-haul cap %d", buyCost, e.perHaulCap)
	}
	if e.fence.Holds(0, buyCost) {
		if e.fence.Unreadable {
			return false, fmt.Sprintf("buy %d refused: live treasury unreadable (fail-closed)", buyCost)
		}
		return false, fmt.Sprintf("buy %d would breach the %d contract-cushion fence (treasury %d)", buyCost, e.fence.Floor, e.fence.Treasury)
	}
	return true, ""
}

// maxConcurrentHauls bounds simultaneously-running long-haul workers so worst-case in-flight
// exposure stays within the total-exposure cap (design guard 3): each worker holds at most
// one perHaulCap tranche at a time, so concurrent × perHaulCap ≤ totalExposureCap. Floored at
// 1 when both caps are positive (a total cap below one haul still runs a single guarded haul).
// A non-positive cap means unlimited (bounded naturally by the tagged fleet size + the
// per-hull guards).
func maxConcurrentHauls(totalExposureCap, perHaulCap int64) int {
	if totalExposureCap <= 0 || perHaulCap <= 0 {
		return 0
	}
	n := int(totalExposureCap / perHaulCap)
	if n < 1 {
		n = 1
	}
	return n
}
