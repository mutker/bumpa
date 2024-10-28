package errors

import (
	"errors"
	"fmt"
	"strings"
)

// Use standard errors.Is and errors.As directly
var (
	Is = errors.Is
	As = errors.As
)

// Error codes
const (
	// System errors
	CodeUnknown       = "unknown_error"  // Unknown error
	CodeInitFailed    = "init_failed"    // Initialization failures
	CodeConfigError   = "config_error"   // Configuration issues
	CodeRuntimeError  = "runtime_error"  // General runtime errors
	CodeTimeoutError  = "timeout_error"  // Timeout related errors
	CodeValidateError = "validate_error" // Validation failures

	// Domain errors
	CodeGitError      = "git_error"      // Git operation failures
	CodeLLMError      = "llm_error"      // LLM related errors
	CodeInputError    = "input_error"    // User input errors
	CodeTemplateError = "template_error" // Template processing errors
	CodeNoChanges     = "no_changes"     // No files staged for commit
	CodeLLMGenFailed  = "llm_gen_failed" // Failed to generate valid commit message
)

// Standard error messages
var errorMessages = map[string]string{
	CodeUnknown:       "unknown error",
	CodeInitFailed:    "initialization failed",
	CodeConfigError:   "configuration error",
	CodeRuntimeError:  "runtime error occurred",
	CodeTimeoutError:  "operation timed out",
	CodeValidateError: "validation failed",
	CodeGitError:      "git operation failed",
	CodeLLMError:      "LLM operation failed",
	CodeInputError:    "invalid input provided",
	CodeTemplateError: "template processing failed",
	CodeNoChanges:     "no changes staged for commit",
	CodeLLMGenFailed:  "failed to generate valid commit message",
}

// Common errors
var (
	ErrNotFound      = errors.New("not found")
	ErrInvalidInput  = errors.New("invalid input")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrInternal      = errors.New("internal error")
	ErrLLMStatus     = errors.New("llm status error")
	ErrInvalidConfig = errors.New("invalid configuration")
	ErrTimeout       = errors.New("timeout")
)

// New creates an error with a code
func New(code string) error {
	return fmt.Errorf("%s: %s", code, errorMessages[code]) //nolint:err113 // Custom error formatting for consistent error messages
}

// Wrap wraps an error with a code and uses standard message
func Wrap(code string, err error) error {
	if err == nil {
		return nil
	}
	msg := errorMessages[code]
	if msg == "" {
		msg = CodeUnknown
	}
	return fmt.Errorf("%s: %s: %w", code, msg, err)
}

// WrapWithContext wraps an error with code and custom context
func WrapWithContext(code string, err error, context string) error {
	if err == nil {
		return nil
	}
	msg := errorMessages[code]
	if msg == "" {
		msg = "unknown error"
	}
	return fmt.Errorf("%s: %s: %s: %w", code, msg, context, err)
}

// GetMessage returns the standard message for an error code
func GetMessage(code string) string {
	if msg, ok := errorMessages[code]; ok {
		return msg
	}
	return CodeUnknown
}

// GetCode extracts the error code from an error
func GetCode(err error) string {
	if err == nil {
		return ""
	}
	parts := strings.SplitN(err.Error(), ":", 2) //nolint:mnd // Split into type+scope and description
	return parts[0]
}

// ErrorMessage returns the standard message for an error code
func ErrorMessage(code string) string {
	if msg, ok := errorMessages[code]; ok {
		return msg
	}
	return CodeUnknown
}

// IsConfigFileNotFound checks if the error is a config file not found error
func IsConfigFileNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Config File") &&
		strings.Contains(err.Error(), "Not Found")
}
