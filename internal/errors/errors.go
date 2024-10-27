package errors

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

const (
	// System errors
	CodeInitFailed   = "init_failed"
	CodeConfigError  = "config_error"
	CodeRuntimeError = "runtime_error"
	CodeTimeoutError = "timeout_error"

	// Git errors
	CodeGitError = "git_error"

	// LLM errors
	CodeLLMError      = "llm_error"
	CodeTemplateError = "template_error"

	// Input/validation errors
	CodeInputError   = "input_error"
	CodeInvalidState = "invalid_state"
)

// Consolidated error messages
var errorMessages = map[string]string{
	CodeInitFailed:    "initialization failed",
	CodeConfigError:   "configuration error",
	CodeRuntimeError:  "runtime error occurred",
	CodeTimeoutError:  "operation timed out",
	CodeGitError:      "git operation failed",
	CodeLLMError:      "LLM operation failed",
	CodeTemplateError: "Template operation failed",
	CodeInputError:    "invalid input",
	CodeInvalidState:  "invalid state",
}

// Common errors
var (
	ErrNotFound     = errors.New("not found")
	ErrInvalidInput = errors.New("invalid input")
	ErrUnauthorized = errors.New("unauthorized")
	ErrInternal     = errors.New("internal error")
)

// New creates an error with a code
func New(code string) error {
	if msg, ok := errorMessages[code]; ok {
		return fmt.Errorf("%s: %s", code, msg) //nolint:err113 // Dynamic error messages required for error code system
	}

	return fmt.Errorf("unknown error: %s", code) //nolint:err113 // Dynamic error messages required for error code system
}

// Wrap wraps an error with a code
func Wrap(code string, err error) error {
	if err == nil {
		return nil
	}
	msg := errorMessages[code]
	if msg == "" {
		return fmt.Errorf("%s: %w", code, err)
	}

	return fmt.Errorf("%s: %s: %w", code, msg, err)
}

// WrapWithContext wraps an error with code and context
func WrapWithContext(code string, err error, context string) error {
	if err == nil {
		return nil
	}
	msg := errorMessages[code]
	if msg == "" {
		return fmt.Errorf("%s: %s: %w", code, context, err)
	}

	return fmt.Errorf("%s: %s: %s: %w", code, msg, context, err)
}

// Is checks if an error matches a code or error
func Is(err error, target interface{}) bool {
	switch t := target.(type) {
	case error:
		return errors.Is(err, t)
	case string:
		return strings.Contains(err.Error(), t+":")
	default:
		return false
	}
}

// IsConfigFileNotFound checks for config file not found
func IsConfigFileNotFound(err error) bool {
	var configFileNotFound viper.ConfigFileNotFoundError
	return errors.As(err, &configFileNotFound)
}

// ErrorMessage gets message for code
func ErrorMessage(code string) string {
	if msg, ok := errorMessages[code]; ok {
		return msg
	}

	return code
}
