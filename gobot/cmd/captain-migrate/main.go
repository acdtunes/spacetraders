package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
)

func main() {
	state := flag.String("state", "../captain/state", "captain state dir")
	reports := flag.String("reports", "../captain/reports/bugs", "bug reports dir")
	apply := flag.Bool("apply", false, "execute the bd commands (default: dry-run, print only)")
	bdBin := flag.String("bd", "bd", "bd binary")
	rig := flag.String("rig", ".", "rig repo root (resolves the sp- db)")
	flag.Parse()

	client := watchkeeper.NewBeadsClient(*bdBin, *rig)
	report, err := watchkeeper.Migrate(context.Background(), client, *state, *reports, *apply)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain-migrate: %v\n", err)
		os.Exit(1)
	}

	if !*apply {
		for _, command := range report.Commands {
			fmt.Println(formatCommand(command))
		}
	}

	summary, marshalErr := json.Marshal(struct {
		Strategy  int  `json:"strategy"`
		Decisions int  `json:"decisions"`
		Lessons   int  `json:"lessons"`
		Backlog   int  `json:"backlog"`
		Bugs      int  `json:"bugs"`
		Commands  int  `json:"commands"`
		Applied   bool `json:"applied"`
	}{report.Strategy, report.Decisions, report.Lessons, report.Backlog, report.Bugs, len(report.Commands), *apply})
	if marshalErr != nil {
		fmt.Fprintf(os.Stderr, "captain-migrate: %v\n", marshalErr)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, string(summary))
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
