package config

import (
	"path/filepath"
	"testing"
)

// TestTwinTestConfigIsolatesFromProduction loads the checked-in digital-twin harness
// config through the REAL loader and pins every value that keeps a test daemon out of
// production's blast radius (the --force PID trap: --force SIGTERM-kills whatever PID is
// in cfg.Daemon.PIDFile; the compiled-in default is production's pidfile).
func TestTwinTestConfigIsolatesFromProduction(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("ST_METRICS_PORT", "")

	path := filepath.Join("..", "..", "..", "..", "twin", "test-config.yaml")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s) failed: %v", path, err)
	}

	checks := []struct{ name, got, want string }{
		{"daemon.pid_file", cfg.Daemon.PIDFile, "/tmp/spacetraders-daemon-test.pid"},
		{"daemon.socket_path", cfg.Daemon.SocketPath, "/tmp/spacetraders-daemon-test.sock"},
		{"daemon.address", cfg.Daemon.Address, "localhost:50062"},
		{"database.url", cfg.Database.URL, "postgresql://spacetraders:dev_password@localhost:5433/spacetraders_test?sslmode=disable"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q — a test daemon booted with this config collides with production", c.name, c.got, c.want)
		}
	}
	if cfg.Metrics.Port != 9092 {
		t.Errorf("metrics.port = %d, want 9092 — production's daemon serves 9090", cfg.Metrics.Port)
	}
	if cfg.Captain.PlayerID != 1 {
		t.Errorf("captain.player_id = %d, want 1 — the seeded TWINAGENT row in a fresh spacetraders_test DB", cfg.Captain.PlayerID)
	}
}
