package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

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
	retryCommand  = "retry"
	quitCommand   = "quit"
)

type UserAction struct {
	Command string
	Message string
}

func main() {
	if err := run(); err != nil {
		logger.Error().Err(err).Msg(errors.GetMessage(errors.CodeRuntimeError))
		os.Exit(1)
	}
}

func run() error {
	// Initialize logging first with initial config
	loggingConfig, err := config.LoadInitialLogging()
	if err != nil {
		return errors.Wrap(errors.CodeInitFailed, err)
	}

	// Convert config to logger.Config
	loggerCfg := logger.Config{
		Environment: loggingConfig.Environment,
		TimeFormat:  loggingConfig.TimeFormat,
		Output:      loggingConfig.Output,
		Level:       loggingConfig.Level,
		Path:        loggingConfig.Path,
		FilePerms:   loggingConfig.FilePerms,
	}

	if err := logger.Init(loggerCfg); err != nil {
		return errors.Wrap(errors.CodeInitFailed, err)
	}

	// Load full configuration
	cfg, err := config.Load()
	if err != nil {
		return errors.Wrap(errors.CodeConfigError, err)
	}

	logger.Debug().
		Str("command", cfg.Command).
		Msg("Configuration loaded")

	ctx := context.Background()

	llmClient, err := initializeLLMClient(cfg)
	if err != nil {
		return err
	}

	repo, err := openGitRepository(cfg)
	if err != nil {
		return err
	}

	if err := executeCommand(ctx, cfg, llmClient, repo); err != nil {
		return errors.Wrap(errors.CodeRuntimeError, err)
	}

	return nil
}

//nolint:ireturn // Interface return needed for flexibility and testing
func initializeLLMClient(cfg *config.Config) (llm.Client, error) {
	llmClient, err := llm.New(&cfg.LLM)
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

func executeCommand(ctx context.Context, cfg *config.Config, llmClient llm.Client, repo *git.Repository) error {
	switch cfg.Command {
	case "commit":
		return runCommit(ctx, cfg, llmClient, repo)
	case "version", "changelog", "pr", "release":
		return errors.WrapWithContext(
			errors.CodeInputError,
			errors.ErrInvalidInput,
			"command not yet implemented",
		)
	default:
		return errors.WrapWithContext(
			errors.CodeInputError,
			errors.ErrInvalidInput,
			fmt.Sprintf("unknown command: %s", cfg.Command), //nolint:perfsprint // More readable than concatenation
		)
	}
}

//nolint:cyclop // Complex function handling git commit workflow
func runCommit(ctx context.Context, cfg *config.Config, llmClient llm.Client, repo *git.Repository) error {
	logger.Info().Msg("Starting commit process")

	generator, err := commit.NewGenerator(cfg, llmClient, repo)
	if err != nil {
		return errors.Wrap(errors.CodeGitError, err)
	}

	filesToCommit, err := repo.GetFilesToCommit()
	if err != nil {
		return errors.Wrap(errors.CodeGitError, err)
	}

	for {
		message, err := generator.Generate(ctx)
		if err != nil {
			if errors.Is(err, errors.ErrInvalidInput) {
				code := errors.GetCode(err)
				switch code {
				case errors.CodeNoChanges:
					logger.Info().Msg("No changes to commit")
				case errors.CodeLLMGenFailed:
					logger.Error().Err(err).Msg("Failed to generate commit message")
				default:
					logger.Error().Err(err).Msg("Unexpected error")
				}
				return nil
			}
			return errors.Wrap(errors.CodeGitError, err)
		}

		userAction, err := promptUserAction(message, filesToCommit)
		if err != nil {
			return errors.Wrap(errors.CodeInputError, err)
		}

		switch userAction.Command {
		case commitCommand, editCommand:
			if err := repo.MakeCommit(ctx, userAction.Message, filesToCommit); err != nil {
				if errors.Is(err, errors.ErrInvalidInput) {
					// For user configuration errors, show the message directly
					fmt.Fprintln(os.Stderr, "\nError:", err)
					return nil
				}
				return errors.Wrap(errors.CodeGitError, err)
			}
			logger.Info().Msg("Commit successfully created")
			return nil
		case retryCommand:
			logger.Info().Msg("Retrying commit message generation")
			continue
		case quitCommand:
			logger.Info().Msg("Commit aborted")
			return nil
		}
	}
}

func promptUserAction(message string, files []string) (UserAction, error) {
	fileList := strings.Join(files, "\n  ")
	prompt := formatPrompt(fileList, message)

	response, err := getUserResponse(prompt)
	if err != nil {
		return UserAction{Command: quitCommand}, errors.Wrap(errors.CodeInputError, err)
	}

	action := processUserResponse(response, message)

	return action, nil
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
		"Do you want to (c)ommit, (e)dit, (r)etry, or (Q)uit? (c/e/r/Q) ", fileList, message)
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

func processUserResponse(response, originalMessage string) UserAction {
	logger.Debug().Str("response", response).Msg("User response")
	switch response {
	case "c":
		return UserAction{Command: commitCommand, Message: originalMessage}
	case "e":
		editedMessage := editCommitMessage(originalMessage)
		return UserAction{Command: editCommand, Message: editedMessage}
	case "r":
		return UserAction{Command: retryCommand, Message: ""}
	default:
		return UserAction{Command: quitCommand, Message: ""}
	}
}
