package config

import (
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
)

// Validator is a wrapper around go-playground/validator
type Validator struct {
	validate *validator.Validate
}

// NewValidator creates a new validator instance with custom validation rules
func NewValidator() *Validator {
	v := validator.New()

	// Register custom validation functions here if needed
	// Example: v.RegisterValidation("custom_rule", customRuleFunc)

	return &Validator{
		validate: v,
	}
}

// Validate validates a struct using validation tags
func (v *Validator) Validate(i interface{}) error {
	if err := v.validate.Struct(i); err != nil {
		return v.formatValidationError(err)
	}
	return nil
}

// formatValidationError converts validator errors into readable messages
func (v *Validator) formatValidationError(err error) error {
	if validationErrs, ok := err.(validator.ValidationErrors); ok {
		var messages []string
		for _, e := range validationErrs {
			messages = append(messages, fmt.Sprintf(
				"field '%s' failed validation: %s (value: '%v')",
				e.Field(),
				e.Tag(),
				e.Value(),
			))
		}
		return fmt.Errorf("validation failed:\n  %s", strings.Join(messages, "\n  "))
	}
	return err
}

// ValidateConfig validates the entire configuration
func ValidateConfig(cfg *Config) error {
	v := NewValidator()
	return v.Validate(cfg)
}
