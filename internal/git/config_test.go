package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/git"
)

func TestRepository_GetUserConfig(t *testing.T) {
	// Create test directory outside of /home/jgabor/git/
	dir := t.TempDir()

	// Save current HOME and GIT_CONFIG_GLOBAL
	oldHome := os.Getenv("HOME")
	oldGitConfig := os.Getenv("GIT_CONFIG_GLOBAL")
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("GIT_CONFIG_GLOBAL", oldGitConfig)
	}()

	// Create temporary home and git config
	tmpHome := t.TempDir()
	tmpGitConfig := tmpHome + "/.gitconfig"
	t.Setenv("HOME", tmpHome)
	t.Setenv("GIT_CONFIG_GLOBAL", tmpGitConfig)

	// Initialize repository
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to initialize git repository: %v", err)
	}

	// Set test user name
	cmd = exec.Command("git", "config", "--local", "user.name", "Test User")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git user name: %v", err)
	}

	// Set test user email
	cmd = exec.Command("git", "config", "--local", "user.email", "test@example.com")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git user email: %v", err)
	}

	// Debug output
	t.Logf("Test directory: %s", dir)
	t.Logf("Temporary HOME: %s", tmpHome)
	t.Logf("Temporary GIT_CONFIG_GLOBAL: %s", tmpGitConfig)

	// Check current git config
	cmd = exec.Command("git", "config", "--list", "--show-origin")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		t.Logf("Failed to list git config: %v", err)
	} else {
		t.Logf("Current git config:\n%s", output)
	}

	// Open repository
	repo, err := git.OpenRepository(dir, config.GitConfig{})
	if err != nil {
		t.Fatalf("OpenRepository() error = %v", err)
	}

	// Test GetUserConfig
	name, email, err := repo.GetUserConfig()
	if err != nil {
		t.Errorf("GetUserConfig() error = %v", err)
		return
	}

	if name != "Test User" {
		t.Errorf("GetUserConfig() name = %v, want %v", name, "Test User")
	}
	if email != "test@example.com" {
		t.Errorf("GetUserConfig() email = %v, want %v", email, "test@example.com")
	}
}

//nolint:cyclop // Test requires complex setup for GPG signing verification
func TestRepository_MakeCommit_WithSigning(t *testing.T) {
	// Skip if GPG signing is not configured
	if !isGPGConfigured(t) {
		t.Skip("GPG signing not configured, skipping test")
	}

	// Create test directory
	dir := t.TempDir()

	// Save and modify environment
	oldHome := os.Getenv("HOME")
	oldGitConfig := os.Getenv("GIT_CONFIG_GLOBAL")
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("GIT_CONFIG_GLOBAL", oldGitConfig)
	}()

	// Use existing git config
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(oldHome, ".gitconfig"))

	// Get current user config
	cmd := exec.Command("git", "config", "--get", "user.name")
	userName, err := cmd.Output()
	if err != nil {
		t.Fatal("failed to get user.name:", err)
	}

	cmd = exec.Command("git", "config", "--get", "user.email")
	userEmail, err := cmd.Output()
	if err != nil {
		t.Fatal("failed to get user.email:", err)
	}

	cmd = exec.Command("git", "config", "--get", "user.signingkey")
	signingKey, err := cmd.Output()
	if err != nil {
		t.Fatal("failed to get user.signingkey:", err)
	}

	// Initialize repository with all required config
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "--local", "user.name", strings.TrimSpace(string(userName))},
		{"git", "config", "--local", "user.email", strings.TrimSpace(string(userEmail))},
		{"git", "config", "--local", "user.signingkey", strings.TrimSpace(string(signingKey))},
		{"git", "config", "--local", "commit.gpgsign", "true"},
	}

	for _, cmd := range cmds {
		//nolint:gosec // Using predefined commands in test context
		c := exec.Command(cmd[0], cmd[1:]...)
		c.Dir = dir
		if err := c.Run(); err != nil {
			t.Fatalf("failed to run %v: %v", cmd, err)
		}
	}

	// Debug output
	t.Logf("Test directory: %s", dir)

	// Show current git config
	cmd = exec.Command("git", "config", "--list", "--show-origin")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err == nil {
		t.Logf("Git config:\n%s", output)
	}

	// Create test file
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Open repository
	repo, err := git.OpenRepository(dir, config.GitConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// Make signed commit
	err = repo.MakeCommit(context.Background(), "Test commit", []string{"test.txt"})
	if err != nil {
		t.Fatal(err)
	}

	// Verify commit is signed
	verifyCmd := exec.Command("git", "verify-commit", "HEAD")
	verifyCmd.Dir = dir
	if output, err := verifyCmd.CombinedOutput(); err != nil {
		t.Errorf("commit was not signed: %v\nOutput: %s", err, output)
	}
}

// isGPGConfigured checks if GPG signing is configured in the current environment
func isGPGConfigured(t *testing.T) bool {
	t.Helper()

	// Check if gpg is available
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Log("gpg not found in PATH")
		return false
	}

	// Check if signing key is configured
	cmd := exec.Command("git", "config", "--get", "user.signingkey")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Logf("user.signingkey not configured: %v\nOutput: %s", err, output)
		return false
	}

	// Verify GPG agent is running
	cmd = exec.Command("gpg-connect-agent", "/bye")
	if err := cmd.Run(); err != nil {
		t.Log("gpg-agent not running")
		return false
	}

	return true
}
