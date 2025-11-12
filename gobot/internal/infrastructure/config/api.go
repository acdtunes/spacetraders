package config

import "time"

// APIConfig holds SpaceTraders API client configuration
type APIConfig struct {
	// Base URL for SpaceTraders API
	BaseURL string `mapstructure:"base_url" validate:"required,url"`

	// Rate limiting settings
	RateLimit RateLimitConfig `mapstructure:"rate_limit"`

	// Request timeout
	Timeout time.Duration `mapstructure:"timeout" validate:"required"`

	// Retry configuration
	Retry RetryConfig `mapstructure:"retry"`
}

// RateLimitConfig holds rate limiting configuration
type RateLimitConfig struct {
	// Maximum requests per second
	Requests int `mapstructure:"requests" validate:"min=1"`

	// Burst size for token bucket
	Burst int `mapstructure:"burst" validate:"min=1"`
}

// RetryConfig holds retry configuration for failed requests
type RetryConfig struct {
	// Maximum number of retry attempts
	MaxAttempts int `mapstructure:"max_attempts" validate:"min=0"`

	// Base duration for exponential backoff
	BackoffBase time.Duration `mapstructure:"backoff_base"`
}
