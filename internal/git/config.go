package git

import (
	"os/exec"
	"strings"

	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/logger"
)

// isGitAvailable checks if git binary is available on the system
func isGitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// getConfigValue retrieves a config value using the best available method
func getConfigValue(scope string, key string) (string, error) {
	// For conditional includes to work properly, we need to run git from the repo directory
	// and let git handle all the config resolution
	if isGitAvailable() {
		value, err := getSystemConfigValue(scope, key)
		if err != nil {
			return "", err
		}
		return value, nil
	}
	return getNativeConfigValue(scope, key)
}

// getSystemConfigValue uses git binary to get config value
func getSystemConfigValue(scope string, key string) (string, error) {
	args := []string{"config"}

	if scope != "" {
		args = append(args, "--"+scope)
	}

	args = append(args, "--get", key)

	cmd := exec.Command("git", args...)
	// Note: We rely on git to handle includeIf and resolve the correct config
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			logger.Debug().
				Str("scope", scope).
				Str("key", key).
				Str("error", string(exitErr.Stderr)).
				Msg("git config command failed")
		}
		return "", nil // Match git behavior: return empty string if key not found
	}

	return strings.TrimSpace(string(out)), nil
}

// getNativeConfigValue uses go-git native implementation
func getNativeConfigValue(_ string, _ string) (string, error) {
	// Currently we don't support includeIf in native mode
	// Always fall back to system git when possible
	return "", nil
}

// getAllConfigValues retrieves all values for a key across all scopes
func getAllConfigValues(key string) (map[string]string, error) {
	// For includeIf support, we primarily care about the effective value
	// rather than distinguishing between scopes
	value, err := getConfigValue("", key) // Let git handle all config resolution
	if err != nil {
		return nil, err
	}

	if value != "" {
		// Return the effective value as if it were from the most specific scope
		return map[string]string{"local": value}, nil
	}

	return nil, errors.WrapWithContext(
		errors.CodeGitError,
		errors.ErrInvalidInput,
		errors.ContextGitUserNotConfigured,
	)
}
