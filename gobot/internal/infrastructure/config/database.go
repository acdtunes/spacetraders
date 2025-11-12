package config

import "time"

// DatabaseConfig holds database connection configuration
type DatabaseConfig struct {
	// Connection type: "postgres" or "sqlite"
	Type string `mapstructure:"type" validate:"required,oneof=postgres sqlite"`

	// Full connection URL (takes precedence over individual fields)
	// Example: postgresql://user:password@localhost:5432/dbname
	URL string `mapstructure:"url"`

	// PostgreSQL connection fields (used if URL is empty)
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port" validate:"omitempty,min=1,max=65535"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	Name     string `mapstructure:"name"`
	SSLMode  string `mapstructure:"sslmode" validate:"omitempty,oneof=disable require verify-ca verify-full"`

	// SQLite connection field
	Path string `mapstructure:"path"`

	// Connection pool settings
	Pool PoolConfig `mapstructure:"pool"`
}

// PoolConfig holds connection pool configuration
type PoolConfig struct {
	MaxOpen     int           `mapstructure:"max_open" validate:"min=1"`
	MaxIdle     int           `mapstructure:"max_idle" validate:"min=1"`
	MaxLifetime time.Duration `mapstructure:"max_lifetime"`
}
