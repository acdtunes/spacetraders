package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

// Config is the main configuration struct combining all sub-configs
type Config struct {
	Database DatabaseConfig `mapstructure:"database"`
	API      APIConfig      `mapstructure:"api"`
	Routing  RoutingConfig  `mapstructure:"routing"`
	Daemon   DaemonConfig   `mapstructure:"daemon"`
	Logging  LoggingConfig  `mapstructure:"logging"`
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
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("./configs")
		v.AddConfigPath("/etc/spacetraders")
	}

	// Enable environment variable reading
	v.SetEnvPrefix("ST") // ST_ prefix for SpaceTraders
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

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
