package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	captainsup "github.com/andrescamacho/spacetraders-go/internal/captain"
)

func main() {
	repo := flag.String("repo", "", "repository root to merge into")
	worktree := flag.String("worktree", "", "worktree directory to gate")
	branch := flag.String("branch", "", "branch to gate and merge")
	message := flag.String("message", "", "squash-merge commit message")
	merge := flag.Bool("merge", false, "squash-merge into repo when the gate passes and the base is fresh")
	provision := flag.Bool("provision", false, "copy gitignored build artifacts into the worktree before gating")
	timeout := flag.Duration("timeout", 20*time.Minute, "gate timeout")
	flag.Parse()

	if *repo == "" || *worktree == "" || *branch == "" {
		fmt.Fprintln(os.Stderr, "captain-gate: --repo, --worktree and --branch are required")
		os.Exit(2)
	}

	if *provision {
		if err := captainsup.ProvisionWorktree(*worktree); err != nil {
			fmt.Fprintf(os.Stderr, "captain-gate: provision: %v\n", err)
			os.Exit(2)
		}
	}

	result, err := captainsup.GateAndMerge(*repo, *worktree, *branch, *message, *timeout, *merge)

	if result.Log != "" {
		fmt.Fprintln(os.Stderr, result.Log)
	}
	out, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		fmt.Fprintf(os.Stderr, "captain-gate: %v\n", marshalErr)
		os.Exit(2)
	}
	fmt.Println(string(out))

	if err != nil {
		fmt.Fprintf(os.Stderr, "captain-gate: %v\n", err)
	}
	if !result.GatePassed || (*merge && !result.Merged) {
		os.Exit(1)
	}
}
