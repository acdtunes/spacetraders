package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

// configPathEnvVar, when set, points config discovery at an explicit config
// file or directory before falling back to the working directory or the
// executable's own directory. Used only when LoadConfig is called with an
// empty configPath.
const configPathEnvVar = "SPACETRADERS_CONFIG"

// Config is the main configuration struct combining all sub-configs
type Config struct {
	Database DatabaseConfig `mapstructure:"database"`
	API      APIConfig      `mapstructure:"api"`
	Routing  RoutingConfig  `mapstructure:"routing"`
	Daemon   DaemonConfig   `mapstructure:"daemon"`
	Logging  LoggingConfig  `mapstructure:"logging"`
	Metrics  MetricsConfig  `mapstructure:"metrics"`
	Captain  CaptainConfig  `mapstructure:"captain"`
	Contract ContractConfig `mapstructure:"contract"`
}

// LoadConfig loads configuration from multiple sources with priority:
// 1. Environment variables (highest priority)
// 2. Config file (config.yaml)
// 3. Defaults (lowest priority)
func LoadConfig(configPath string) (*Config, error) {
	// Load .env file if it exists (doesn't error if missing)
	_ = godotenv.Load()

	v := viper.New()

	// Set config file details
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else if search := resolveConfigSearch(os.Getenv(configPathEnvVar), executableDir()); search.file != "" {
		v.SetConfigFile(search.file)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		for _, path := range search.paths {
			v.AddConfigPath(path)
		}
	}

	// Enable environment variable reading
	v.SetEnvPrefix("ST") // ST_ prefix for SpaceTraders
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Explicitly bind nested environment variables (viper doesn't auto-bind nested structs)
	// Metrics
	v.BindEnv("metrics.enabled", "ST_METRICS_ENABLED")
	v.BindEnv("metrics.port", "ST_METRICS_PORT")
	v.BindEnv("metrics.host", "ST_METRICS_HOST")
	v.BindEnv("metrics.path", "ST_METRICS_PATH")

	// Read config file (optional - don't error if missing)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		// Config file not found is OK - we'll use env vars and defaults
	}

	// Special handling for DATABASE_URL environment variable
	// This allows users to set the full connection string without ST_ prefix
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		v.Set("database.url", dbURL)
	}

	// Create config struct and unmarshal
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Apply defaults for any missing values
	SetDefaults(&cfg)

	// Resolve the tour market-model artifact path to an ABSOLUTE location relative to
	// the config file's directory (sp-wj0h): the tour executor reads it at launch and
	// the launchd daemon's cwd is not the repo root, so a cwd-relative path DOA's the
	// engine on "no such file or directory".
	cfg.Routing.ModelArtifactPath = resolveModelArtifactPath(v.ConfigFileUsed(), cfg.Routing.ModelArtifactPath)

	// Validate configuration
	if err := ValidateConfig(&cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// LoadConfigOrDefault loads configuration or returns a default config on error
func LoadConfigOrDefault(configPath string) *Config {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		// Return default configuration
		defaultCfg := &Config{}
		SetDefaults(defaultCfg)
		return defaultCfg
	}
	return cfg
}

// MustLoadConfig loads configuration and panics on error (for use in main.go)
func MustLoadConfig(configPath string) *Config {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		panic(fmt.Sprintf("failed to load configuration: %v", err))
	}
	return cfg
}

// resolveModelArtifactPath resolves the tour market-model artifact path to an absolute
// location so the tour executor can read it regardless of the daemon's cwd (sp-wj0h).
// When a config file was used, its directory anchors the resolution — config.yaml lives
// at gobot/config.yaml and the artifact at gobot/services/..., so a config-dir-relative
// default is correct in dev AND deploy:
//   - empty current    → <config-dir>/services/routing-service/model_artifacts/market_model.json
//   - relative current → <config-dir>/<current>
//   - absolute current → unchanged
//
// When no config file was used (pure-env boot), current is returned unchanged so the
// coordinator falls back to its repo-relative constant (behavior unchanged for that path).
func resolveModelArtifactPath(configFile, current string) string {
	if configFile == "" {
		return current
	}
	dir := filepath.Dir(configFile)
	if current == "" {
		return filepath.Join(dir, "services", "routing-service", "model_artifacts", "market_model.json")
	}
	if filepath.IsAbs(current) {
		return current
	}
	return filepath.Join(dir, current)
}

// configSearch describes where viper should look for a config file. When file
// is non-empty it names an explicit config file (SetConfigFile) that wins
// outright; otherwise paths is an ordered list of directories to search
// (AddConfigPath), first match wins.
type configSearch struct {
	file  string
	paths []string
}

// resolveConfigSearch computes the config discovery strategy for callers that
// pass an empty configPath. The resolution order (highest priority first) is:
//
//  1. envOverride (the SPACETRADERS_CONFIG value): a file is used directly; a
//     directory is searched before all others.
//  2. Current working directory: ".", "./configs", "/etc/spacetraders". This is
//     the pre-existing behavior, kept exactly so callers running with cwd=gobot
//     (daemon, watchkeeper) resolve config.yaml identically to before.
//  3. execDir and its parent, so a `spacetraders` symlink on PATH still finds
//     the config.yaml shipped next to the real binary (bin/spacetraders ->
//     ../config.yaml). Omitted when execDir is empty.
//
// It is pure with respect to its inputs (the only filesystem access is a stat
// of envOverride to distinguish a file from a directory), so the ordering can
// be exercised without real binaries.
func resolveConfigSearch(envOverride, execDir string) configSearch {
	var s configSearch

	if envOverride != "" {
		if info, err := os.Stat(envOverride); err == nil && !info.IsDir() {
			// An explicit file override is decisive.
			s.file = envOverride
			return s
		}
		// A directory (or a not-yet-existing path) is searched ahead of the cwd.
		s.paths = append(s.paths, envOverride)
	}

	// Legacy working-directory search paths — must stay first and unchanged.
	s.paths = append(s.paths, ".", "./configs", "/etc/spacetraders")

	// Fall back to the executable's own directory and its parent.
	if execDir != "" {
		s.paths = append(s.paths, execDir, filepath.Dir(execDir))
	}

	return s
}

// osExecutable is indirected so tests can simulate the running binary's
// location without building a real executable.
var osExecutable = os.Executable

// executableDir returns the directory containing the running executable with
// symlinks resolved, or "" if it cannot be determined. Resolving symlinks means
// a PATH shim (e.g. ~/.local/bin/spacetraders -> .../gobot/bin/spacetraders)
// points at the real binary's directory, whose parent holds config.yaml.
func executableDir() string {
	exe, err := osExecutable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}
