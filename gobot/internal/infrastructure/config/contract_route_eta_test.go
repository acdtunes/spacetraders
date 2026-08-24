package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
)

// The route-ETA budget is a tunable, not a feature: it is in force with no config
// present, and the knob only moves it. These tests exercise the REAL viper pipeline
// (contract.route_eta_budget_ms -> Config.Contract.RouteETABudgetMs) and then the exact
// expression the daemon's composition root passes to the estimator, so the whole seam
// from file to enforced budget is pinned in one place.
//
// appContract is imported by the TEST only. Nothing in the config package's production
// surface depends on the application layer, and this file must not become the reason it does.

func TestLoadConfig_RouteETABudget_AbsentRunsTheInCodeDefault(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	// No [contract] section at all — the shape of a config predating the knob.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("database:\n  host: h\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	estimator := appContract.NewRouteETAEstimator(nil, nil, cfg.Contract.RouteETABudget())
	require.Equal(t, 6*time.Second, estimator.Budget(),
		"an absent knob must leave the estimator on its in-code default, never on a zero budget that expires before the first route answers")
}

func TestLoadConfig_RouteETABudget_ExplicitValueReachesTheEstimator(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("contract:\n  route_eta_budget_ms: 4500\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, 4500, cfg.Contract.RouteETABudgetMs,
		"route_eta_budget_ms must reach the config struct so an operator retunes the budget by editing config.yaml + restarting")
	estimator := appContract.NewRouteETAEstimator(nil, nil, cfg.Contract.RouteETABudget())
	require.Equal(t, 4500*time.Millisecond, estimator.Budget())
}

func TestContractConfig_RouteETABudget_NonPositiveDefersToTheEstimator(t *testing.T) {
	// A negative or zero setting is not "no budget" — it defers, and the estimator
	// supplies the default. Returning the sentinel here is what keeps the default in
	// exactly one place.
	require.Equal(t, time.Duration(0), ContractConfig{RouteETABudgetMs: 0}.RouteETABudget())
	require.Equal(t, time.Duration(0), ContractConfig{RouteETABudgetMs: -1}.RouteETABudget())
}
