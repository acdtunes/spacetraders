package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
)

// regimePolicyStore is the subset of captain regime-tripwire persistence the
// CLI needs. It exists so runRegimeSet/runRegimeList/runRegimeClear can be
// tested with a fake, without touching the filesystem.
type regimePolicyStore interface {
	Load() (watchkeeper.RegimePolicy, error)
	Save(policy watchkeeper.RegimePolicy) error
}

// fileRegimePolicyStore is the production regimePolicyStore, backed by the
// same supervisor state file the WakePolicy uses (see
// internal/captain/state.go) — the two policies are independent owners
// sharing that one file, so a regime write never clobbers a wake write or
// vice versa.
type fileRegimePolicyStore struct {
	path string
}

func (f fileRegimePolicyStore) Load() (watchkeeper.RegimePolicy, error) {
	return watchkeeper.LoadRegimePolicy(f.path)
}

func (f fileRegimePolicyStore) Save(policy watchkeeper.RegimePolicy) error {
	return watchkeeper.SaveRegimePolicy(f.path, policy)
}

// newCaptainRegimePolicyStore resolves the production store via the same
// state-path lookup the wake-policy store uses (captainWakeStatePath,
// defined in captain_ops.go) — regime tripwires and the wake policy live in
// the same supervisor state file, just different fields of it.
func newCaptainRegimePolicyStore() (regimePolicyStore, error) {
	path, err := captainWakeStatePath()
	if err != nil {
		return nil, err
	}
	return fileRegimePolicyStore{path: path}, nil
}

// parseRegimeThreshold parses a --bid-above/--bid-below flag value as either
// an absolute integer price ("200") or a multiplier of a recorded baseline
// price ("3x", "3.5x") — dual-mode parsing mirroring parseNextWakeAt's
// relative/absolute idiom. Exactly one of the two return pointers is
// non-nil on success.
func parseRegimeThreshold(s string) (threshold *int, multiplier *float64, err error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil, nil, fmt.Errorf("threshold value is empty")
	}
	if rest, ok := strings.CutSuffix(strings.ToLower(trimmed), "x"); ok {
		m, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid multiplier %q: %w", s, err)
		}
		return nil, &m, nil
	}
	v, err := strconv.Atoi(trimmed)
	if err != nil {
		return nil, nil, fmt.Errorf("threshold %q must be an integer price or a multiplier like \"3x\": %w", s, err)
	}
	return &v, nil, nil
}

// runRegimeSet appends a new tripwire to the persisted list. This is
// deliberately additive, unlike "captain wake set"'s full-replace semantics:
// regime tripwires are independent list items (the captain's own motivating
// example declares an ORE tripwire AND a GAS tripwire simultaneously), so a
// second "regime set" call must not silently drop the first. Overlapping
// tripwires on the same good+direction are harmless — detectRegimeShift
// dedups by (good, market, direction), not by which tripwire config matched.
func runRegimeSet(store regimePolicyStore, now time.Time, tw watchkeeper.RegimeTripwire) error {
	policy, err := store.Load()
	if err != nil {
		return fmt.Errorf("failed to load regime policy: %w", err)
	}
	tw.CreatedAt = now
	policy.Tripwires = append(policy.Tripwires, tw)
	return store.Save(policy)
}

// runRegimeList renders every currently-declared tripwire to w.
func runRegimeList(store regimePolicyStore, w io.Writer) error {
	policy, err := store.Load()
	if err != nil {
		return fmt.Errorf("failed to load regime policy: %w", err)
	}
	if len(policy.Tripwires) == 0 {
		_, err := fmt.Fprintln(w, "No price tripwires declared (regime detector is inactive).")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "GOOD\tDIRECTION\tTHRESHOLD\tWINDOW\tCREATED_AT")
	for _, t := range policy.Tripwires {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			t.Good, t.Direction, formatRegimeThreshold(t), t.Window, t.CreatedAt.Format(time.RFC3339))
	}
	return tw.Flush()
}

func formatRegimeThreshold(t watchkeeper.RegimeTripwire) string {
	switch {
	case t.Threshold != nil:
		return strconv.Itoa(*t.Threshold)
	case t.Multiplier != nil:
		return fmt.Sprintf("%gx baseline", *t.Multiplier)
	default:
		return "(unset)"
	}
}

// runRegimeClear deletes every declared tripwire, disabling the detector
// (no config means no scan — see detectRegimeShift).
func runRegimeClear(store regimePolicyStore) error {
	return store.Save(watchkeeper.RegimePolicy{})
}

func newCaptainRegimeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "regime",
		Short: "Inspect or declare the captain's price-regime tripwires",
		Long: `Manage the captain's price-regime tripwires (spec: sp-zlfv price-regime
detector). A tripwire is a standing "watch this good's sell price" rule: the
watchkeeper emits a deferred market.regime_shift event once a matching good
crosses the declared threshold, mechanizing the per-wake price sweep the
captain used to hand-roll.

Tripwires are additive: "regime set" adds one without disturbing the others,
"regime list" prints every declared tripwire, and "regime clear" removes them
all (with none declared, the detector does not scan at all). The declared set
lives in the supervisor state file and survives restarts.

Examples:
  spacetraders captain regime set --good ORE --bid-above 200
  spacetraders captain regime list
  spacetraders captain regime clear`,
	}

	cmd.AddCommand(newCaptainRegimeSetCommand())
	cmd.AddCommand(newCaptainRegimeListCommand())
	cmd.AddCommand(newCaptainRegimeClearCommand())

	return cmd
}

func newCaptainRegimeSetCommand() *cobra.Command {
	var good string
	var bidAbove string
	var bidBelow string
	var window time.Duration

	cmd := &cobra.Command{
		Use:   "set --good <ORE|GAS|SYMBOL[,SYMBOL...]> (--bid-above <price|Nx> | --bid-below <price|Nx>) [--window 1h]",
		Short: "Declare a price tripwire (adds to, does not replace, existing tripwires)",
		Long: `Declare a captain price tripwire (spec: sp-zlfv price-regime detector): the
watchkeeper emits a deferred market.regime_shift event once a matching good's
market price crosses in the given direction. Mechanizes the per-wake price
sweep the captain used to hand-roll ("any ore bid >=200 or gas bid >=150
(~3x baseline) triggers an immediate extraction re-consult").

Unlike "captain wake set" (full-replace), each "regime set" call ADDS a new
tripwire to the declared list — it does not remove previously declared
tripwires. Run "captain regime clear" first if you want a clean slate.

--good accepts a class keyword ("ORE" matches any *_ORE symbol; "GAS" matches
the fixed hydrocarbon/liquid-gas set) or a literal comma-separated symbol
list ("IRON_ORE,COPPER_ORE") for an exact match only.

--bid-above/--bid-below (exactly one required) accept either an absolute
sell price ("200") or a multiplier of a recorded baseline price ("3x").
Multiplier mode looks up the OLDEST price-history sample within --window as
the baseline; it will not fire until at least one such sample has been
recorded.

--window serves two purposes: the baseline lookback in multiplier mode, and
the edge-trigger cooldown in both modes — once a crossing fires, the same
good+market+direction will not re-fire until --window elapses and the price
re-crosses (avoids a session-burn loop on every poll while the price stays
crossed).

Examples:
  spacetraders captain regime set --good ORE --bid-above 200
  spacetraders captain regime set --good GAS --bid-above 3x --window 4h
  spacetraders captain regime set --good HYDROCARBON --bid-below 20`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(good) == "" {
				return fmt.Errorf("--good is required")
			}
			aboveSet := cmd.Flags().Changed("bid-above")
			belowSet := cmd.Flags().Changed("bid-below")
			if aboveSet == belowSet {
				return fmt.Errorf("specify exactly one of --bid-above or --bid-below")
			}

			direction, raw := "bid-above", bidAbove
			if belowSet {
				direction, raw = "bid-below", bidBelow
			}
			threshold, multiplier, err := parseRegimeThreshold(raw)
			if err != nil {
				return err
			}

			store, err := newCaptainRegimePolicyStore()
			if err != nil {
				return err
			}
			if err := runRegimeSet(store, time.Now(), watchkeeper.RegimeTripwire{
				Good:       good,
				Direction:  direction,
				Threshold:  threshold,
				Multiplier: multiplier,
				Window:     window,
			}); err != nil {
				return fmt.Errorf("failed to save regime tripwire: %w", err)
			}
			fmt.Println("Price tripwire declared.")
			return nil
		},
	}

	cmd.Flags().StringVar(&good, "good", "", `Good class ("ORE", "GAS") or comma-separated literal symbol(s)`)
	cmd.Flags().StringVar(&bidAbove, "bid-above", "", `Fire when sell price rises to or above this: absolute price or a multiplier like "3x"`)
	cmd.Flags().StringVar(&bidBelow, "bid-below", "", `Fire when sell price falls to or below this: absolute price or a multiplier like "3x"`)
	cmd.Flags().DurationVar(&window, "window", time.Hour, "Baseline lookback (multiplier mode) and edge-trigger cooldown")

	return cmd
}

func newCaptainRegimeListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the captain's currently declared price tripwires",
		Long: `List every price tripwire currently declared via "captain regime set".

Examples:
  spacetraders captain regime list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainRegimePolicyStore()
			if err != nil {
				return err
			}
			return runRegimeList(store, os.Stdout)
		},
	}
	return cmd
}

func newCaptainRegimeClearCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Remove all declared price tripwires (disables the regime detector)",
		Long: `Remove every currently-declared price tripwire. With no tripwires declared,
the watchkeeper's price-regime detector does not scan at all.

Examples:
  spacetraders captain regime clear`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainRegimePolicyStore()
			if err != nil {
				return err
			}
			if err := runRegimeClear(store); err != nil {
				return fmt.Errorf("failed to clear regime policy: %w", err)
			}
			fmt.Println("Price tripwires cleared.")
			return nil
		},
	}
	return cmd
}
