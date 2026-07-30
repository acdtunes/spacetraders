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

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func main() {
	once := flag.Bool("once", false, "run a single supervisor tick and exit")
	flag.Parse()

	// Build stamp first, before any config/DB work: captain-supervisor.log then
	// always names the live binary's commit even if startup later fails, and a
	// deploy can grep it to assert the fresh build is running.
	fmt.Println(buildinfo.Get().Banner("watchkeeper"))

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
	ws := watchkeeper.NewWorkspace(cfg.Captain.WorkspaceDir)
	sup, err := watchkeeper.NewSupervisor(db, store, ws, cfg.Captain)
	if err != nil {
		log.Fatalf("watchkeeper: %v", err)
	}

	gw := watchkeeper.NewCityGateway(cfg.Captain.GCBin, cfg.Captain.CityDir)
	bc := watchkeeper.NewBeadsClient(cfg.Captain.BDBin, cfg.Captain.RepoDir)
	sup.SetCity(gw, bc)

	// sp-k4z5b: mirror the daemon's market-scan allowance so the planner-staleness
	// detector resolves the SAME freshness boundary the tour planner does. The boundary is
	// an OUTPUT of budget / markets known, not a constant, so a detector holding a fixed
	// minute count reports fiction the moment the map outgrows it.
	sup.SetMarketScanBudget(marketscan.Budget{
		RateReqPerSec: cfg.MarketScan.ResolvedBudgetReqPerSec(),
		ValueClampR:   cfg.MarketScan.ResolvedValueClampR(),
	})

	apiClient := api.NewSpaceTradersClient()
	sup.SetUniverseWatch(apiClient, persistence.NewEraRepository(db))

	// Plumb live agent credits into the wake gate: the captain
	// sizes its credit thresholds from what `player info` shows (the live agent
	// API), so the supervisor must evaluate that same number, not a divergent
	// ledger reconstruction. Best-effort: if the token lookup fails the
	// supervisor falls back to the reconstruction.
	//
	// The token is resolved ONCE here. Players/tokens rotate across eras, so a
	// supervisor left running across an era reset will fail every live fetch and
	// degrade to the retained/reconstructed value; the daemon must be restarted
	// on era close to pick up the new token (see SetAgentAPI). The universe-reset
	// kill-switch wired just above halts the fleet on the reset it detects.
	if playerID, perr := shared.NewPlayerID(cfg.Captain.PlayerID); perr != nil {
		fmt.Printf("watchkeeper: WARNING invalid captain player_id %d, live credits disabled: %v\n",
			cfg.Captain.PlayerID, perr)
	} else if p, perr := persistence.NewGormPlayerRepository(db).FindByID(context.Background(), playerID); perr != nil {
		fmt.Printf("watchkeeper: WARNING could not resolve captain player token, live credits disabled: %v\n", perr)
	} else {
		sup.SetAgentAPI(apiClient, p.Token)
	}

	// Regenerate the CLI reference so sessions never see a stale command surface
	// (spec: Tool discovery §1). Best-effort: a missing binary must not stop the
	// supervisor, it only degrades tool discovery to --help fallback.
	if out, err := exec.Command("./scripts/gen-cli-reference.sh", "./bin/spacetraders",
		cfg.Captain.WorkspaceDir+"/CLI_REFERENCE.md").CombinedOutput(); err != nil {
		fmt.Printf("warning: CLI reference regeneration failed: %v: %s\n", err, out)
	}

	fmt.Printf("Watchkeeper starting (player=%d workspace=%s model=%s)\n",
		cfg.Captain.PlayerID, cfg.Captain.WorkspaceDir, cfg.Captain.Model)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// One cheap probe of the wake-delivery channel at startup: an
	// env-broken gc/bd (e.g. a launch environment missing BD_REAL) is otherwise
	// only discoverable by reading generic per-tick errors. Never blocks
	// startup — the channel may recover.
	sup.Preflight(ctx)

	if *once {
		ran, err := sup.Tick(ctx, time.Now())
		fmt.Printf("tick: ran=%v err=%v\n", ran, err)
		return
	}
	if err := sup.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("supervisor: %v", err)
	}
}
