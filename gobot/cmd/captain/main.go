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
		return captainsup.NewClaudeRunner(
			cfg.Captain.ClaudeBin, cfg.Captain.Model, workDir,
			time.Duration(cfg.Captain.FixSessionTimeoutMinutes)*time.Minute,
		)
	}
	sup.SetFixer(captainsup.NewFixer(ws, fixerFactory, cfg.Captain))

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
