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
func getConfigValue(scope, key string) (string, error) {
	// For conditional includes to work properly, we need to run git from the repo directory
	// and let git handle all the config resolution
	if isGitAvailable() {
		return getSystemConfigValue(scope, key), nil
	}
	return getNativeConfigValue(scope, key)
}

// getSystemConfigValue uses git binary to get config value
func getSystemConfigValue(scope, key string) string {
	args := []string{"config"}

	if scope != "" {
		args = append(args, "--"+scope)
	}

	args = append(args, "--get", key)

	cmd := exec.Command("git", args...)
	// Note: We rely on git to handle includeIf and resolve the correct config
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			logger.Debug().
				Str("scope", scope).
				Str("key", key).
				Str("error", string(exitErr.Stderr)).
				Msg("git config command failed")
		}
		return "" // Match git behavior: return empty string if key not found
	}

	return strings.TrimSpace(string(out))
}

// getNativeConfigValue uses go-git native implementation
func getNativeConfigValue(_, _ string) (string, error) {
	// Currently we don't support includeIf in native mode
	// Always fall back to system git when possible
	return "", nil
}
