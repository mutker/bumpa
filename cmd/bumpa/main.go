package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"codeberg.org/mutker/bumpa/internal/commit"
	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/git"
	"codeberg.org/mutker/bumpa/internal/llm"
	"codeberg.org/mutker/bumpa/internal/logger"
)

const (
	commitCommand = "commit"
	editCommand   = "edit"
	quitCommand   = "quit"
)

type UserAction struct {
	Command string
	Message string
}

func main() {
	if err := run(); err != nil {
		logger.Error().Err(err).Msg(errors.ErrorMessage(errors.CodeRuntimeError))
		os.Exit(1)
	}
}

func run() error {
	if err := initializeLogger(); err != nil {
		return errors.Wrap(errors.CodeInitFailed, err)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger.Debug().
		Str("command", cfg.Command).
		Interface("config", cfg).
		Msg("Configuration loaded")

	ctx := context.Background()

	llmClient, err := initializeLLMClient(ctx, cfg)
	if err != nil {
		return err
	}

	repo, err := openGitRepository(cfg)
	if err != nil {
		return err
	}

	return executeCommand(ctx, cfg, llmClient, repo)
}

func initializeLogger() error {
	err := logger.Init("development", time.RFC3339, "console", "info", "")
	if err != nil {
		return errors.Wrap(errors.CodeInitFailed, err)
	}

	return nil
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, errors.Wrap(errors.CodeConfigError, err)
	}

	return cfg, nil
}

//nolint:ireturn // Interface return needed for flexibility and testing
func initializeLLMClient(ctx context.Context, cfg *config.Config) (llm.Client, error) {
	llmClient, err := llm.New(ctx, &cfg.LLM)
	if err != nil {
		return nil, errors.Wrap(errors.CodeLLMError, err)
	}

	return llmClient, nil
}

func openGitRepository(cfg *config.Config) (*git.Repository, error) {
	repo, err := git.OpenRepository(".", cfg.Git)
	if err != nil {
		return nil, errors.Wrap(errors.CodeGitError, err)
	}

	return repo, nil
}

//nolint:wrapcheck // Using WrapWithContext for command-specific error context
func executeCommand(ctx context.Context, cfg *config.Config, llmClient llm.Client, repo *git.Repository) error {
	switch cfg.Command {
	case "commit":
		return runCommit(ctx, cfg, llmClient, repo)
	case "version", "changelog", "pr", "release":
		return errors.New(errors.CodeInputError)
	default:
		return errors.WrapWithContext(
			errors.CodeInputError,
			errors.ErrInvalidInput,
			"unknown command: "+cfg.Command,
		)
	}
}

func runCommit(ctx context.Context, cfg *config.Config, llmClient llm.Client, repo *git.Repository) error {
	logger.Info().Msg("Starting commit process")

	generator := commit.NewGenerator(cfg, llmClient, repo)

	message, err := generator.Generate(ctx)
	if err != nil {
		if errors.Is(err, errors.CodeInvalidState) {
			logger.Info().Msg(errors.ErrorMessage(errors.CodeInvalidState))
			return nil
		}

		return errors.Wrap(errors.CodeGitError, err)
	}

	filesToCommit, err := repo.GetFilesToCommit()
	if err != nil {
		return errors.Wrap(errors.CodeGitError, err)
	}

	userAction, err := promptUserAction(message, filesToCommit)
	if err != nil {
		return errors.Wrap(errors.CodeInputError, err)
	}

	switch userAction.Command {
	case commitCommand:
		if err := repo.MakeCommit(ctx, userAction.Message, filesToCommit); err != nil {
			return errors.Wrap(errors.CodeGitError, err)
		}
		logger.Info().Msg("Commit successfully created")
	case editCommand:
		if err := repo.MakeCommit(ctx, userAction.Message, filesToCommit); err != nil {
			return errors.Wrap(errors.CodeGitError, err)
		}
		logger.Info().Msg("Commit successfully created with edited message")
	case quitCommand:
		logger.Info().Msg("Commit aborted")
	}

	logger.Info().Msg("Commit process completed successfully")

	return nil
}

func promptUserAction(message string, files []string) (UserAction, error) {
	fileList := strings.Join(files, "\n  ")
	prompt := formatPrompt(fileList, message)
	response, err := getUserResponse(prompt)
	if err != nil {
		return UserAction{Command: quitCommand}, errors.Wrap(errors.CodeInputError, err)
	}

	return processUserResponse(response, message)
}

func editCommitMessage(message string) string {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}

	tempFile, err := os.CreateTemp("", "COMMIT_EDITMSG")
	if err != nil {
		logger.Error().Err(err).Msg("failed to create temporary file")
		return message
	}
	defer os.Remove(tempFile.Name())

	if _, err := tempFile.WriteString(message); err != nil {
		logger.Error().Err(err).Msg("failed to write to temporary file")
		return message
	}
	tempFile.Close()

	cmd := exec.Command(editor, tempFile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		logger.Error().Err(err).Msg("failed to run editor")
		return message
	}

	editedContent, err := os.ReadFile(tempFile.Name())
	if err != nil {
		logger.Error().Err(err).Msg("failed to read edited file")
		return message
	}

	return strings.TrimSpace(string(editedContent))
}

func formatPrompt(fileList, message string) string {
	return fmt.Sprintf("Files to commit:\n  %s\n\nCommit message:\n  %s\n\n"+
		"Do you want to (c)ommit, (e)dit, or (Q)uit? (c/e/Q) ", fileList, message)
}

//nolint:forbidigo // Direct console interaction required
func getUserResponse(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return "", errors.Wrap(errors.CodeInputError, err)
	}

	return strings.TrimSpace(strings.ToLower(response)), nil
}

func processUserResponse(response, originalMessage string) (UserAction, error) {
	logger.Debug().Str("response", response).Msg("User response")
	switch response {
	case "c":
		return UserAction{Command: commitCommand, Message: originalMessage}, nil
	case "e":
		editedMessage := editCommitMessage(originalMessage)
		return UserAction{Command: editCommand, Message: editedMessage}, nil
	default:
		return UserAction{Command: quitCommand, Message: ""}, nil
	}
}
