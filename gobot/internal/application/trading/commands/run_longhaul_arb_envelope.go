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

	// defaultLongHaulTotalExposureCap is the total in-flight long-haul exposure figure (~2M).
	// It is threaded to each worker for parity only — the coordinator applies no concurrency
	// ceiling from it (Admiral uncap order); spend stays bounded per buy by the fence below.
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
// fence. No total-exposure ceiling is enforced anywhere (Admiral uncap order) — a worker
// holds at most one perHaulCap tranche at a time (it sells the out cargo before buying the
// backhaul), so the per-haul + cushion bounds here are the whole spend guard.
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
