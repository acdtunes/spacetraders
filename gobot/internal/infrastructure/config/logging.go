package config

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	// Log level: debug, info, warn, error
	Level string `mapstructure:"level" validate:"required,oneof=debug info warn error"`

	// Log format: json, text
	Format string `mapstructure:"format" validate:"required,oneof=json text"`

	// Output destination: stdout, stderr, file
	Output string `mapstructure:"output" validate:"required,oneof=stdout stderr file"`

	// File path (required if output is "file")
	FilePath string `mapstructure:"file_path"`

	// Enable file rotation
	Rotation RotationConfig `mapstructure:"rotation"`

	// Include caller information (file:line)
	IncludeCaller bool `mapstructure:"include_caller"`

	// Include stack traces for errors
	IncludeStacktrace bool `mapstructure:"include_stacktrace"`
}

// RotationConfig holds log file rotation configuration
type RotationConfig struct {
	// Enable rotation
	Enabled bool `mapstructure:"enabled"`

	// Maximum size in megabytes before rotation
	MaxSize int `mapstructure:"max_size" validate:"min=1"`

	// Maximum number of old log files to keep
	MaxBackups int `mapstructure:"max_backups" validate:"min=0"`

	// Maximum age in days before deletion
	MaxAge int `mapstructure:"max_age" validate:"min=0"`

	// Compress rotated files
	Compress bool `mapstructure:"compress"`
}
