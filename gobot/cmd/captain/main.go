package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	captainsup "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func main() {
	once := flag.Bool("once", false, "run a single supervisor tick and exit")
	flag.Parse()

	cfg := config.MustLoadConfig("")
	if !cfg.Captain.Enabled && !*once {
		log.Fatal("captain.enabled is false in config; refusing to start (use --once to force a single tick)")
	}
	if cfg.Captain.PlayerID == 0 {
		log.Fatal("captain.player_id must be set in config")
	}

	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close(db)

	store := persistence.NewGormCaptainEventRepository(db)
	ws := captainsup.NewWorkspace(cfg.Captain.WorkspaceDir)
	runner := captainsup.NewClaudeRunner(
		cfg.Captain.ClaudeBin,
		cfg.Captain.Model,
		cfg.Captain.WorkspaceDir,
		time.Duration(cfg.Captain.SessionTimeoutMinutes)*time.Minute,
	)
	sup := captainsup.NewSupervisor(db, store, runner, ws, cfg.Captain)

	fixerFactory := func(workDir string) captainsup.SessionRunner {
		r := captainsup.NewClaudeRunner(
			cfg.Captain.ClaudeBin, cfg.Captain.FixModel, workDir,
			time.Duration(cfg.Captain.FixSessionTimeoutMinutes)*time.Minute,
		)
		// Worktree paths are untrusted workspaces (allowlists ignored), so
		// headless fix sessions could not even edit files. The supervisor-run
		// gate is the safety boundary for these throwaway trees.
		r.ExtraArgs = []string{"--dangerously-skip-permissions"}
		return r
	}
	fixer := captainsup.NewFixer(ws, fixerFactory, cfg.Captain)
	// A prior supervisor may have died mid-build, stranding a report at
	// in_progress that the pipeline would otherwise never retry. Reclaim them.
	if n := fixer.RecoverOrphanedFixes(); n > 0 {
		fmt.Printf("Captain: recovered %d orphaned fix report(s) at startup\n", n)
	}
	sup.SetFixer(fixer)

	if cfg.Captain.EngineMode == "bridge" {
		gw := captainsup.NewCityGateway(cfg.Captain.GCBin, cfg.Captain.CityDir)
		bc := captainsup.NewBeadsClient(cfg.Captain.BDBin, cfg.Captain.RepoDir)
		sup.SetCity(gw, bc)
	}

	// Regenerate the CLI reference so sessions never see a stale command surface
	// (spec: Tool discovery §1). Best-effort: a missing binary must not stop the
	// supervisor, it only degrades tool discovery to --help fallback.
	if out, err := exec.Command("./scripts/gen-cli-reference.sh", "./bin/spacetraders",
		cfg.Captain.WorkspaceDir+"/CLI_REFERENCE.md").CombinedOutput(); err != nil {
		fmt.Printf("warning: CLI reference regeneration failed: %v: %s\n", err, out)
	}

	fmt.Printf("Captain supervisor starting (player=%d workspace=%s model=%s)\n",
		cfg.Captain.PlayerID, cfg.Captain.WorkspaceDir, cfg.Captain.Model)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *once {
		ran, err := sup.Tick(ctx, time.Now())
		fmt.Printf("tick: ran=%v err=%v\n", ran, err)
		return
	}
	if err := sup.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("supervisor: %v", err)
	}
}
