package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

// stampCadence projects the ledger's parked slots into the scan rotation,
// applying the quartermaster cadence to yard slots. The cadence is a knob and
// therefore resolved here rather than in the adapter, which reads columns only.
func (h *RunProbeSensingCoordinatorHandler) stampCadence(views []parkedsensing.SensingSlotView, cfg sensingConfig) []parkedsensing.SensingSlotView {
	out := make([]parkedsensing.SensingSlotView, 0, len(views))
	for _, view := range views {
		if view.Kind == parkedsensing.SlotKindYard {
			view.YardCadence = cfg.QuartermasterCadence
		}
		out = append(out, view)
	}
	return out
}

// ensurePacer starts the container's scan pacer unless one is already running.
// It is called from every reconcile, and both halves of that matter.
//
// IDEMPOTENT, because Handle can run more than once for a single container. The
// container runner re-sends the SAME command — same container id, same
// uncancelled context — after an error or a panic, up to MaxRestartAttempts, so
// a pacer launched from Handle would be launched again on each retry. Two pacers
// popping one heap issue scans at twice the rate the budget arithmetic computed,
// and the heartbeat cannot show it: it reports the rate it HANDED to the
// rotation, not the rate being spent. The fleet would simply overrun its share
// of the rate limiter with nothing anywhere saying so.
//
// SELF-HEALING, because the panic guard around the pacer suppresses and returns
// rather than restarting. A single panic would otherwise stop all parked-market
// scanning for the life of the container while every heartbeat still reported a
// healthy computed rate — the failure would surface only as market data ageing
// without bound, hours later, on the staleness gauge. Re-checking here converts
// that into a one-tick outage with a loud line naming it.
func (h *RunProbeSensingCoordinatorHandler) ensurePacer(ctx context.Context, cyc sensingCycle) {
	scanner := h.scannerFor(cyc)

	h.mu.Lock()
	if h.pacersRunning[cyc.cmd.ContainerID] {
		h.mu.Unlock()
		return
	}
	h.pacersRunning[cyc.cmd.ContainerID] = true
	run := h.runPacer
	h.mu.Unlock()

	go func() {
		defer h.pacerStopped(ctx, cyc.cmd)
		supervise.Guard(pacerGuardComponent+cyc.cmd.ContainerID, func() { run(ctx, scanner) })
	}()
}

// pacerStopped releases the container's pacer slot and, when the coordinator is
// still meant to be running, reports the death loudly.
//
// A cancelled context is an ordinary shutdown and says nothing. Anything else
// means the pacer returned or panicked while the fleet still expected it to be
// scanning, which is exactly the silent failure the re-check above exists to
// bound — so it is logged at ERROR with the relaunch stated, rather than left to
// be inferred from a staleness gauge.
func (h *RunProbeSensingCoordinatorHandler) pacerStopped(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) {
	h.mu.Lock()
	delete(h.pacersRunning, cmd.ContainerID)
	h.mu.Unlock()

	if ctx.Err() != nil {
		return // the coordinator is shutting down; the pacer stopping is correct
	}
	common.LoggerFromContext(ctx).Log("ERROR", fmt.Sprintf(
		"Parked-probe scan pacer for %s stopped while the coordinator is still running — every parked market has stopped being scanned; the next reconcile relaunches it",
		cmd.ContainerID), map[string]interface{}{
		"action":       "parked_sensing_pacer_died",
		"container_id": cmd.ContainerID,
	})
}

// pacerLive reports whether the container currently holds a pacer.
func (h *RunProbeSensingCoordinatorHandler) pacerLive(containerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pacersRunning[containerID]
}

// scannerFor returns the container's scan rotation, creating it on first use.
//
// InflightCap and ClampR bind at construction, so a tune of either applies at
// the coordinator's next rebuild rather than the next tick. That is deliberate:
// both shape the rotation's normalisation, and swapping them under a live pacer
// would re-pace every slot mid-flight. Every other knob is live.
func (h *RunProbeSensingCoordinatorHandler) scannerFor(cyc sensingCycle) *parkedsensing.Scanner {
	h.mu.Lock()
	defer h.mu.Unlock()

	if scanner, ok := h.scanners[cyc.cmd.ContainerID]; ok {
		return scanner
	}
	scanner := parkedsensing.NewScanner(cyc.cmd.PlayerID.Value(), parkedsensing.ScanPorts{
		Scan:     cyc.ports.Scan,
		Ledger:   cyc.ports.Ledger,
		SpreadOf: cyc.ports.SpreadOf,
		// The scanning-tagged yard read, so a parked probe records the shipyard
		// under its feet on the same turn it reads the market there. It is the only
		// path that ever PRICES a yard we occupy.
		Yard: cyc.ports.YardScan,
	}, h.clock, parkedsensing.ScanKnobs{
		InflightCap: cyc.cfg.InflightCap,
		ClampR:      cyc.cfg.ClampR,
	})
	h.scanners[cyc.cmd.ContainerID] = scanner
	return scanner
}
