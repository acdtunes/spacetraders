package config

// MetricsConfig holds metrics collection and exposure configuration
type MetricsConfig struct {
	// Enabled controls whether metrics collection is active
	Enabled bool `mapstructure:"enabled"`

	// Port for the HTTP metrics server (Prometheus endpoint)
	Port int `mapstructure:"port" validate:"omitempty,min=1024,max=65535"`

	// Host to bind the metrics HTTP server (default: localhost for security)
	Host string `mapstructure:"host"`

	// Path for the metrics endpoint (default: /metrics)
	Path string `mapstructure:"path"`
}
