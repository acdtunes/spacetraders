package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// NewSensingCommand builds the `sensing` verb family for the parked-probe sensing
// model. It is deliberately NOT hung off `scout`: that group belongs to the
// touring model this one retired, and putting a parked-model verb there would
// point an operator at the wrong engine.
func NewSensingCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sensing",
		Short: "Parked-probe sensing: operator maintenance for the sensing ledger",
		Long: `Operator verbs for the parked-probe sensing model.

A parked probe is bought for one waypoint, flown there once, and then stands
still forever scanning its own market. Which waypoints earn a probe is decided by
screening each system against the goods whitelist in config.yaml's [sensing]
block — and those verdicts are durable, which is what makes the rescreen verb
below necessary after the whitelist changes.`,
	}
	cmd.AddCommand(newSensingRescreenCommand())
	return cmd
}

// newSensingRescreenCommand is the operator response to a mid-era whitelist edit
// (sp-j2efq).
func newSensingRescreenCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rescreen",
		Short: "Re-open every system verdict so the sweep re-judges under the current whitelist",
		Long: `Return every sensing system verdict to PENDING for re-screening.

WHEN YOU NEED THIS: after editing [sensing] goods_whitelist in config.yaml. Every
verdict is stamped with the whitelist in force when it was written, and
NO_WHITELIST is durable — only PENDING systems are ever re-screened — so a changed
list does NOT re-open the systems it would now accept. Editing the config alone
changes nothing about the existing map; this is what applies it.

WHAT IT TOUCHES: sensing_systems.verdict, and nothing else. Placements, the hulls
standing on them, the scan history and any in-flight charting errand all survive
— a rescreen re-evaluates judgement, never ownership.

WHAT YOU WILL SEE AFTERWARDS, so it does not read as a fault: every verdict is
PENDING for a while, and until the sweep re-screens a system the buy queue treats
it as out of scope. So probe buying pauses fleet-wide, buy_reaped spikes as the
reaper hands back claims that are momentarily undrainable, and the queue looks
stalled. That is the rescreen working, not failing — no hull is disturbed and no
money guard is touched. It clears as the sweep works through at five systems per
tick, which on a large map is tens of minutes.

WHAT IT DOES NOT COVER: a market NO PROBE HAS EVER SCANNED is judged from the
goods projection recorded on its slot, and this verb leaves that projection alone
(clearing it would be permanent and would suppress the refetch that repopulates
it — see sp-ysg8h). So a remotely-discovered placement keeps its old projection
until a probe parks there and scans it, after which the market cache answers and
the next re-screen judges it correctly on its own. Every market a probe HAS
visited is already re-judged correctly from that cache — which is every parked
placement, so nothing you are already paying for is affected.

Safe to run at any time and safe to repeat — the cost of a needless run is one
re-screen sweep, which the coordinator already paces at five systems per tick. The
new verdicts land over the following ticks, not instantly.

Examples:
  spacetraders sensing rescreen --agent ENDURANCE
  spacetraders sensing rescreen --player-id 1`,
		RunE: func(_ *cobra.Command, _ []string) error {
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()

			playerID, agentSymbol := playerPointers(playerIdent)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			var pid int32
			if playerID != nil {
				pid = *playerID
			}
			resp, err := client.SensingRescreen(ctx, pid, agentSymbol)
			if err != nil {
				return fmt.Errorf("sensing rescreen failed: %w", err)
			}

			if resp.SystemsReopened == 0 {
				fmt.Println("Sensing rescreen: no system verdicts to re-open (nothing has been screened yet).")
				return nil
			}
			fmt.Printf("Sensing rescreen: %d system verdict(s) re-opened for screening under the current whitelist.\n",
				resp.SystemsReopened)
			fmt.Println("The coordinator re-screens up to 5 systems per tick, so the new verdicts land over the next few ticks.")
			return nil
		},
	}
	return cmd
}
