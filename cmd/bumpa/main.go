package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"codeberg.org/mutker/bumpa/internal/commit"
	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/git"
	"codeberg.org/mutker/bumpa/internal/llm"
	"codeberg.org/mutker/bumpa/pkg/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Define a custom flag set
	flagSet := flag.NewFlagSet("bumpa", flag.ExitOnError)

	// Define flags
	// You can add more flags here if needed

	// Parse flags
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		return fmt.Errorf("failed to parse flags: %w", err)
	}

	// Check if a command was provided
	if flagSet.NArg() < 1 {
		return fmt.Errorf("no command specified. Available commands: commit")
	}

	// Get the command
	command := flagSet.Arg(0)

	// Execute the appropriate command
	switch command {
	case "commit":
		return runCommit()
	default:
		return fmt.Errorf("unknown command: %s", command)
	}
}

func runCommit() error {
	ctx := context.Background()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Initialize LLM client
	llmClient, err := llm.New(ctx, cfg.LLM)
	if err != nil {
		return fmt.Errorf("failed to initialize LLM client: %w", err)
	}

	// Open Git repository
	repo, err := git.OpenRepository(".")
	if err != nil {
		return fmt.Errorf("failed to open git repository: %w", err)
	}

	// Create commit generator
	generator := commit.NewGenerator(cfg, llmClient, logger.Logger, repo.GetRepo())

	// Generate commit message
	message, err := generator.Generate()
	if err != nil {
		if errors.Is(err, git.ErrNoChanges) {
			logger.Logger.Info().Msg("No changes to commit")
			return nil
		}
		return fmt.Errorf("failed to generate commit message: %w", err)
	}

	// Get files to commit
	filesToCommit, err := repo.GetFilesToCommit()
	if err != nil {
		if errors.Is(err, git.ErrNoChanges) {
			logger.Logger.Info().Msg("No changes to commit")
			return nil
		}
		return fmt.Errorf("failed to get files to commit: %w", err)
	}

	// Prompt user for action
	action, editedMessage := promptUserAction(message, filesToCommit)
	switch action {
	case "commit":
		if err := repo.MakeCommit(message, filesToCommit); err != nil {
			return fmt.Errorf("failed to commit: %w", err)
		}
		logger.Logger.Info().Msg("Commit successfully created")
	case "edit":
		if err := repo.MakeCommit(editedMessage, filesToCommit); err != nil {
			return fmt.Errorf("failed to commit: %w", err)
		}
		logger.Logger.Info().Msg("Commit successfully created with edited message")
	case "quit":
		logger.Logger.Info().Msg("Commit aborted")
	}

	return nil
}

// promptUserAction function from the original main.go
func promptUserAction(message string, files []string) (string, string) {
	logger.Logger.Debug().Str("message", message).Msg("Prompting user for action")

	fileList := strings.Join(files, "\n  ")
	prompt := fmt.Sprintf("Files to commit:\n  %s\n\nCommit message:\n  %s\n\nDo you want to (c)ommit, (e)dit, or (Q)uit? (c/e/Q) ", fileList, message)

	fmt.Print(prompt)

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to read user input")
		return "quit", ""
	}

	response = strings.TrimSpace(strings.ToLower(response))
	logger.Logger.Debug().Str("response", response).Msg("User response")

	switch response {
	case "c":
		return "commit", message
	case "e":
		editedMessage := editCommitMessage(message)
		return "edit", editedMessage
	default:
		return "quit", ""
	}
}

// editCommitMessage function from the original main.go
func editCommitMessage(message string) string {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim" // Default to vim if no EDITOR is set
	}

	tempFile, err := os.CreateTemp("", "COMMIT_EDITMSG")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to create temporary file")
		return message
	}
	defer os.Remove(tempFile.Name())

	if _, err := tempFile.WriteString(message); err != nil {
		logger.Logger.Error().Err(err).Msg("failed to write to temporary file")
		return message
	}
	tempFile.Close()

	cmd := exec.Command(editor, tempFile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		logger.Logger.Error().Err(err).Msg("failed to run editor")
		return message
	}

	editedContent, err := os.ReadFile(tempFile.Name())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to read edited file")
		return message
	}

	return strings.TrimSpace(string(editedContent))
}
