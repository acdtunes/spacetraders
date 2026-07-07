package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
)

func main() {
	era := flag.String("era", "", "era name, lowercase agent symbol (e.g. torwind)")
	agent := flag.String("agent", "", "agent symbol for ship-instance marker detection (e.g. TORWIND)")
	resetDate := flag.String("reset-date", "", "universe resetDate (YYYY-MM-DD), used in the close reason")
	windowStart := flag.String("window-start", "", "date-window start (YYYY-MM-DD), inclusive")
	windowEnd := flag.String("window-end", "", "date-window end (YYYY-MM-DD), inclusive")
	apply := flag.Bool("apply", false, "execute the planned label/close commands (default: dry-run, print only). Memory actions are never executed.")
	bdBin := flag.String("bd", "bd", "bd binary")
	rig := flag.String("rig", ".", "rig repo root (resolves the sp- db)")
	flag.Parse()

	if strings.TrimSpace(*era) == "" {
		fmt.Fprintln(os.Stderr, "era-close: --era is required")
		os.Exit(1)
	}

	start, err := parseDate(*windowStart, time.Unix(0, 0).UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "era-close: --window-start: %v\n", err)
		os.Exit(1)
	}
	end, err := parseDate(*windowEnd, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "era-close: --window-end: %v\n", err)
		os.Exit(1)
	}

	client := watchkeeper.NewBeadsClient(*bdBin, *rig)
	rep, err := watchkeeper.EraClose(context.Background(), client, *era, *resetDate, start, end, *agent, *apply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "era-close: %v\n", err)
		os.Exit(1)
	}

	for _, command := range rep.Commands {
		fmt.Println(formatCommand(command))
	}

	fmt.Println()
	fmt.Println("MEMORY PROPOSAL (Admiral approval required; never auto-applied)")
	fmt.Println("KEY\tACTION\tREASON")
	for _, p := range rep.MemoryProposals {
		fmt.Printf("%s\t%s\t%s\n", p.Key, p.Action, p.Reason)
	}

	summary, marshalErr := json.Marshal(struct {
		Era             string `json:"era"`
		Labeled         int    `json:"labeled"`
		Closed          int    `json:"closed"`
		StrategyBead    string `json:"strategy_bead"`
		MemoryProposals int    `json:"memory_proposals"`
		Applied         bool   `json:"applied"`
	}{rep.EraName, len(rep.Labeled), len(rep.Closed), rep.StrategyBead, len(rep.MemoryProposals), *apply})
	if marshalErr != nil {
		fmt.Fprintf(os.Stderr, "era-close: %v\n", marshalErr)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, string(summary))
}

func parseDate(value string, fallback time.Time) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return time.Parse("2006-01-02", value)
}

func formatCommand(command []string) string {
	parts := make([]string, len(command))
	for i, arg := range command {
		if strings.ContainsAny(arg, " \t") {
			parts[i] = fmt.Sprintf("%q", arg)
		} else {
			parts[i] = arg
		}
	}
	return strings.Join(parts, " ")
}
