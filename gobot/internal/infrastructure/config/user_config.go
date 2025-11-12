package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// UserConfig represents user preferences stored in ~/.spacetraders/config.json
// This file stores ONLY preferences, never tokens or secrets
type UserConfig struct {
	// Default player ID to use when not specified via CLI
	DefaultPlayerID *int `json:"default_player_id,omitempty"`

	// Default agent symbol to use when not specified via CLI
	DefaultAgent string `json:"default_agent,omitempty"`
}

// UserConfigHandler manages loading and saving user configuration
type UserConfigHandler struct {
	configPath string
}

// NewUserConfigHandler creates a new user config handler
func NewUserConfigHandler() (*UserConfigHandler, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	configDir := filepath.Join(homeDir, ".spacetraders")
	configPath := filepath.Join(configDir, "config.json")

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %w", err)
	}

	return &UserConfigHandler{
		configPath: configPath,
	}, nil
}

// Load reads the user config from disk
func (h *UserConfigHandler) Load() (*UserConfig, error) {
	// If file doesn't exist, return empty config
	if _, err := os.Stat(h.configPath); os.IsNotExist(err) {
		return &UserConfig{}, nil
	}

	data, err := os.ReadFile(h.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read user config: %w", err)
	}

	var config UserConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse user config: %w", err)
	}

	return &config, nil
}

// Save writes the user config to disk
func (h *UserConfigHandler) Save(config *UserConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal user config: %w", err)
	}

	if err := os.WriteFile(h.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write user config: %w", err)
	}

	return nil
}

// SetDefaultPlayer sets the default player ID
func (h *UserConfigHandler) SetDefaultPlayer(playerID int) error {
	config, err := h.Load()
	if err != nil {
		return err
	}

	config.DefaultPlayerID = &playerID
	return h.Save(config)
}

// SetDefaultAgent sets the default agent symbol
func (h *UserConfigHandler) SetDefaultAgent(agent string) error {
	config, err := h.Load()
	if err != nil {
		return err
	}

	config.DefaultAgent = agent
	return h.Save(config)
}

// ClearDefaultPlayer removes the default player setting
func (h *UserConfigHandler) ClearDefaultPlayer() error {
	config, err := h.Load()
	if err != nil {
		return err
	}

	config.DefaultPlayerID = nil
	config.DefaultAgent = ""
	return h.Save(config)
}

// GetConfigPath returns the path to the user config file
func (h *UserConfigHandler) GetConfigPath() string {
	return h.configPath
}
