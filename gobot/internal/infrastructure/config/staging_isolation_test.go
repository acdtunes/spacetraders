package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// Known PROD identifiers (sp-widl). These are the live values the prod daemon
// resolves to — set both by SetDefaults (see defaults.go) and by the operator's
// local gobot/config.yaml, and by the launchd unit (com.spacetraders.*). config.yaml
// itself is an untracked local file, so the staging isolation contract is pinned
// against these documented literals rather than by loading the untracked prod file
// (which is absent in CI / a fresh checkout).
const (
	prodDaemonSocket  = "/tmp/spacetraders-daemon.sock"
	prodDaemonPID     = "/tmp/spacetraders-daemon.pid"
	prodDaemonAddress = "localhost:50052"
	prodRoutingAddr   = "localhost:50051"
	prodMetricsPort   = 9090
	prodDatabaseName  = "spacetraders"
)

// loadStagingConfig loads the committed staging config from the gobot root
// (three levels up from this package directory) with the env overrides that
// could contaminate the result cleared, so the test reads exactly what the
// committed file declares.
func loadStagingConfig(t *testing.T) *Config {
	t.Helper()
	// A stray SPACETRADERS_CONFIG/DATABASE_URL in the environment must not steer
	// the loader away from the explicit file under test.
	t.Setenv("SPACETRADERS_CONFIG", "")
	t.Setenv("DATABASE_URL", "")
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "config.staging.yaml"))
	if err != nil {
		t.Fatalf("abs path for config.staging.yaml: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(config.staging.yaml): %v", err)
	}
	return cfg
}

// TestStagingIsolatedFromProd pins the sp-widl isolation contract: every mutable
// resource the committed staging config names must DIFFER from the live prod
// identifier, so a staging daemon can never collide with or corrupt prod. If a
// future edit points staging at a live resource, this fails loudly in CI instead
// of at runtime on the live fleet.
func TestStagingIsolatedFromProd(t *testing.T) {
	stg := loadStagingConfig(t)

	// Self-documenting proof: -v prints the resolved identifiers for both envs.
	t.Logf("resolved isolation identifiers (prod | staging):")
	t.Logf("  database.name : %-30s | %s", prodDatabaseName, stg.Database.Name)
	t.Logf("  database.url  : %-30s | %s", "<local config.yaml>/spacetraders", stg.Database.URL)
	t.Logf("  daemon.socket : %-30s | %s", prodDaemonSocket, stg.Daemon.SocketPath)
	t.Logf("  daemon.pid    : %-30s | %s", prodDaemonPID, stg.Daemon.PIDFile)
	t.Logf("  daemon.address: %-30s | %s", prodDaemonAddress, stg.Daemon.Address)
	t.Logf("  routing.addr  : %-30s | %s", prodRoutingAddr, stg.Routing.Address)
	t.Logf("  metrics.port  : %-30d | %d", prodMetricsPort, stg.Metrics.Port)

	// Each staging identifier must differ from the corresponding prod literal.
	disjoint := []struct {
		name, prod, stg string
	}{
		{"daemon.socket_path", prodDaemonSocket, stg.Daemon.SocketPath},
		{"daemon.pid_file", prodDaemonPID, stg.Daemon.PIDFile},
		{"daemon.address", prodDaemonAddress, stg.Daemon.Address},
		{"routing.address", prodRoutingAddr, stg.Routing.Address},
		{"database.name", prodDatabaseName, stg.Database.Name},
	}
	for _, c := range disjoint {
		if c.stg == "" {
			t.Errorf("%s: staging value is empty (must be an explicit staging resource)", c.name)
		}
		if c.stg == c.prod {
			t.Errorf("%s: staging shares the prod value %q — NOT isolated", c.name, c.prod)
		}
	}
	if stg.Metrics.Port == prodMetricsPort {
		t.Errorf("metrics.port: staging shares the prod value %d — NOT isolated", prodMetricsPort)
	}
	if stg.Metrics.Port == 0 {
		t.Errorf("metrics.port: staging value is 0 (must be an explicit staging port)")
	}

	// The staging database URL must resolve to the staging database, never the
	// live one (buildPostgresDSN uses URL verbatim when set).
	if !strings.Contains(stg.Database.URL, prodDatabaseName+"_staging") {
		t.Errorf("database.url = %q, expected to target %s_staging", stg.Database.URL, prodDatabaseName)
	}
	if strings.Contains(stg.Database.URL, "/"+prodDatabaseName+"?") ||
		strings.HasSuffix(stg.Database.URL, "/"+prodDatabaseName) {
		t.Errorf("database.url = %q targets the LIVE database %q — NOT isolated", stg.Database.URL, prodDatabaseName)
	}

	// Positive assertions: the staging identifiers self-identify as staging, so a
	// glance (or a grep) can never confuse a staging resource for a prod one.
	positives := []struct{ name, got, want string }{
		{"daemon.socket_path", stg.Daemon.SocketPath, "staging"},
		{"daemon.pid_file", stg.Daemon.PIDFile, "staging"},
		{"database.name", stg.Database.Name, "staging"},
	}
	for _, p := range positives {
		if !strings.Contains(p.got, p.want) {
			t.Errorf("%s = %q, expected to contain %q (staging must self-identify)", p.name, p.got, p.want)
		}
	}

	// The staging DB name must be exactly spacetraders_staging (the boundary the
	// bring-up/teardown scripts and their guards all key on).
	if stg.Database.Name != "spacetraders_staging" {
		t.Errorf("database.name = %q, want spacetraders_staging", stg.Database.Name)
	}
}
