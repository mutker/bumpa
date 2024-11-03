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

// Error codes - Layer 1
const (
	// System errors
	CodeUnknown       = "unknown_error"
	CodeInitFailed    = "init_failed"
	CodeConfigError   = "config_error"
	CodeRuntimeError  = "runtime_error"
	CodeTimeoutError  = "timeout_error"
	CodeValidateError = "validate_error"
	CodeVersionError  = "version_error"

	// Domain errors
	CodeGitError      = "git_error"
	CodeLLMError      = "llm_error"
	CodeInputError    = "input_error"
	CodeTemplateError = "template_error"
	CodeNoChanges     = "no_changes"
	CodeLLMGenFailed  = "llm_gen_failed"
)

// Common error messages - Layer 2
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
	CodeVersionError:  "version operation failed",
}

// Base errors - Layer 3
var (
	ErrNotFound      = errors.New("not found")
	ErrInvalidInput  = errors.New("invalid input")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrInternal      = errors.New("internal error")
	ErrLLMStatus     = errors.New("LLM status error")
	ErrInvalidConfig = errors.New("invalid configuration")
	ErrTimeout       = errors.New("timeout")
)

// Error contexts - Layer 4
const (
	// Configuration contexts
	ContextConfigNotFound        = "config file not found"
	ContextConfigUnmarshal       = "failed to unmarshal configuration"
	ContextInvalidLogLevel       = "invalid log level specified"
	ContextInvalidTimeFormat     = "invalid time format specified"
	ContextMissingFunctionConfig = "required function configuration missing"
	ContextMissingPrompt         = "missing %s prompt for function: %s" // system/user
	ContextMissingAPIKey         = "API key required for %s provider"

	// Git contexts
	ContextNoChanges            = "no changes staged for commit - use 'git add' to stage files"
	ContextGitUserNotConfigured = "git user not configured - run: git config --global user.name '<name>' " +
		"&& git config --global user.email '<email>'"
	ContextGitRepoOpen          = "failed to open git repository"
	ContextGitWorkTree          = "failed to get git worktree"
	ContextGitStatus            = "failed to get git status"
	ContextGitCommit            = "failed to create git commit"
	ContextGitBranch            = "failed to get current branch"
	ContextGitDiff              = "failed to get file diff"
	ContextGitIgnore            = "failed to read gitignore patterns"
	ContextGitConfigInvalidMode = "invalid git config mode"
	ContextGitConfigReadError   = "failed to read git config"
	ContextGitConfigWriteError  = "failed to write git config"
	ContextGitSigningDisabled   = "git commit signing is disabled"
	ContextGitSigningFailed     = "failed to sign git commit"
	ContextGitSigningKey        = "failed to get git signing key"
	ContextGitSigningConfig     = "failed to read git signing configuration"
	ContextGitFileDeleted       = "file has been deleted: %s"
	ContextGitFileRenamed       = "file has been renamed from %s to %s"
	ContextGitFileNotFound      = "file not found in repository: %s"
	ContextGitFileStatus        = "file status: %s"
	ContextGitDiffTruncated     = "diff truncated at %d lines"

	// LLM contexts
	ContextLLMRequest         = "failed to make LLM request"
	ContextLLMResponse        = "failed to decode LLM response"
	ContextLLMNoChoices       = "no choices in LLM response"
	ContextLLMEmptyResponse   = "empty response from LLM function"
	ContextLLMInvalidResponse = "invalid response format from LLM"
	ContextLLMRateLimit       = "rate limit exceeded"
	ContextLLMTimeout         = "LLM request timed out"
	ContextLLMGeneration      = "failed to generate commit message: %s"
	ContextLLMRetryMessage    = "LLM is struggling to generate a valid commit message - " +
		"try running the command again, make the changes smaller, or commit manually"
	// Command contexts
	ContextNoCommand      = "no command specified"
	ContextInvalidCommand = "unknown command: %s"
	ContextNotImplemented = "command not yet implemented"
	ContextCommandFailed  = "command execution failed"

	// File operation contexts
	ContextFileCreate = "failed to create file: %s"
	ContextFileRead   = "failed to read file: %s"
	ContextFileWrite  = "failed to write file: %s"
	ContextFileDelete = "failed to delete file: %s"
	ContextDirCreate  = "failed to create directory: %s"

	// Version bump contexts
	ContextVersionAnalyze    = "failed to analyze version changes"
	ContextVersionParse      = "failed to parse version suggestion"
	ContextVersionInvalid    = "invalid version format"
	ContextVersionPreRelease = "invalid pre-release format: %s"
	ContextVersionBumpType   = "invalid bump type: %s"
	ContextVersionPropose    = "failed to propose version change"
	ContextVersionApply      = "failed to apply version change"
)

// Helper functions
func FormatContext(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}

// Error creation and wrapping
func New(code string) error {
	return fmt.Errorf("%s: %s", code, errorMessages[code]) //nolint:err113 // Custom error formatting
}

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

// Error information retrieval
func GetMessage(code string) string {
	if msg, ok := errorMessages[code]; ok {
		return msg
	}
	return CodeUnknown
}

func GetCode(err error) string {
	if err == nil {
		return ""
	}
	parts := strings.SplitN(err.Error(), ":", 2) //nolint:mnd // Split into type+scope and description
	return parts[0]
}

// Error type checking
func IsConfigFileNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), ContextConfigNotFound)
}

func IsGitSigningError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, ContextGitSigningFailed) ||
		strings.Contains(errStr, ContextGitSigningKey) ||
		strings.Contains(errStr, ContextGitSigningConfig)
}

func IsGitConfigError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, ContextGitConfigReadError) ||
		strings.Contains(errStr, ContextGitConfigWriteError) ||
		strings.Contains(errStr, ContextGitConfigInvalidMode)
}

func IsGitFileOperation(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, ContextGitFileDeleted) ||
		strings.Contains(errStr, ContextGitFileRenamed) ||
		strings.Contains(errStr, ContextGitFileNotFound)
}

func IsNoChanges(err error) bool {
	return GetCode(err) == CodeNoChanges
}

func IsLLMError(err error) bool {
	return GetCode(err) == CodeLLMError
}

func IsVersionError(err error) bool {
	return GetCode(err) == CodeVersionError
}

func IsVersionBumpTypeError(err error) bool {
	return err != nil && strings.Contains(err.Error(), ContextVersionBumpType)
}

func IsVersionPreReleaseError(err error) bool {
	return err != nil && strings.Contains(err.Error(), ContextVersionPreRelease)
}

func IsTimeoutError(err error) bool {
	return GetCode(err) == CodeTimeoutError
}
