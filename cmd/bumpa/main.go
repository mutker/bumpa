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
	"codeberg.org/mutker/bumpa/internal/version"
)

const (
	commitCommand = "commit"
	editCommand   = "edit"
	retryCommand  = "retry"
	quitCommand   = "quit"
)

type CommitAction struct {
	Command string
	Message string
}

type VersionAction struct {
	Command    string
	BumpType   string
	PreRelease string
}

func main() {
	if err := run(); err != nil {
		// If we haven't initialized logging yet, fall back to stderr
		if logger.IsInitialized() {
			logger.Error().Err(err).Msg(errors.GetMessage(errors.CodeRuntimeError))
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
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

func executeCommand(ctx context.Context, cfg *config.Config, llmClient llm.Client, repo *git.Repository) error {
	switch cfg.Command {
	case "commit":
		return runCommit(ctx, cfg, llmClient, repo)
	case "version":
		return runVersion(ctx, cfg, llmClient, repo)
	case "changelog", "pr", "release":
		return errors.WrapWithContext(
			errors.CodeInputError,
			errors.ErrInvalidInput,
			"command not yet implemented",
		)
	default:
		return errors.WrapWithContext(
			errors.CodeInputError,
			errors.ErrInvalidInput,
			"unknown command: "+cfg.Command,
		)
	}
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

func promptCommitAction(message string, files []string) (CommitAction, error) {
	fileList := strings.Join(files, "\n  ")
	prompt := formatPrompt(fileList, message)

	response, err := getUserResponse(prompt)
	if err != nil {
		return CommitAction{Command: quitCommand}, errors.Wrap(errors.CodeInputError, err)
	}

	action := processCommitResponse(response, message)

	return action, nil
}

func processCommitResponse(response, originalMessage string) CommitAction {
	logger.Debug().Str("response", response).Msg("User response")
	switch response {
	case "c":
		return CommitAction{Command: commitCommand, Message: originalMessage}
	case "e":
		editedMessage := editContent(originalMessage, "COMMIT")
		return CommitAction{Command: editCommand, Message: editedMessage}
	case "r":
		return CommitAction{Command: retryCommand}
	default:
		return CommitAction{Command: quitCommand}
	}
}

func processVersionResponse(response, proposed string) VersionAction {
	logger.Debug().Str("response", response).Msg("User response")
	switch response {
	case "a", "c":
		return VersionAction{Command: commitCommand}
	case "e":
		// Let user edit the full version string
		editedVersion := editContent(proposed, "VERSION")
		logger.Debug().
			Str("original", proposed).
			Str("edited", editedVersion).
			Msg("Version edited")

		// Split into version and pre-release parts
		parts := strings.Split(editedVersion, "-")
		baseVersion := parts[0]
		var preRelease string
		if len(parts) > 1 {
			preRelease = parts[1]
		}

		// Determine bump type by comparing with current version
		var bumpType string
		if strings.HasPrefix(baseVersion, "0.1.0") {
			bumpType = "minor"
		} else if strings.HasPrefix(baseVersion, "0.0.2") {
			bumpType = "patch"
		} else {
			bumpType = "" // No change in base version
		}

		return VersionAction{
			Command:    editCommand,
			BumpType:   bumpType,
			PreRelease: preRelease,
		}
	case "r":
		return VersionAction{Command: retryCommand}
	default:
		return VersionAction{Command: quitCommand}
	}
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

func editContent(content, prefix string) string {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}

	tempFile, err := os.CreateTemp("", prefix+"_EDIT")
	if err != nil {
		logger.Error().Err(err).Msg("failed to create temporary file")
		return content
	}
	defer os.Remove(tempFile.Name())

	if _, err := tempFile.WriteString(content); err != nil {
		logger.Error().Err(err).Msg("failed to write to temporary file")
		return content
	}
	tempFile.Close()

	cmd := exec.Command(editor, tempFile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		logger.Error().Err(err).Msg("failed to run editor")
		return content
	}

	editedContent, err := os.ReadFile(tempFile.Name())
	if err != nil {
		logger.Error().Err(err).Msg("failed to read edited file")
		return content
	}

	return strings.TrimSpace(string(editedContent))
}

func formatPrompt(fileList, message string) string {
	return fmt.Sprintf("Files to commit:\n  %s\n\nCommit message:\n  %s\n\n"+
		"Do you want to (c)ommit, (e)dit, (r)etry, or (Q)uit? (c/e/r/Q) ", fileList, message)
}

//nolint:cyclop // Complex function handling git commit workflow
func runCommit(ctx context.Context, cfg *config.Config, llmClient llm.Client, repo *git.Repository) error {
	generator, err := commit.NewGenerator(cfg, llmClient, repo)
	if err != nil {
		return errors.Wrap(errors.CodeGitError, err)
	}

	for {
		// Get current workflow state
		state, err := generator.GetWorkflowState(ctx)
		if err != nil {
			if errors.Is(err, errors.ErrInvalidInput) {
				logger.Info().Msg("No changes to commit")
				return nil
			}
			return errors.Wrap(errors.CodeGitError, err)
		}

		// Early exit if no changes
		if !state.HasChanges {
			logger.Info().Msg("No changes to commit")
			return nil
		}

		// Build prompt based on workflow state
		prompt := buildCommitPrompt(state)

		// Get user response
		response, err := getUserResponse(prompt)
		if err != nil {
			return errors.Wrap(errors.CodeInputError, err)
		}

		// Handle user action
		switch response {
		case "c": // commit
			if !state.CanCommit {
				logger.Warn().Msg("Cannot commit: invalid message or no changes")
				continue
			}

			if err := repo.MakeCommit(ctx, state.Message, state.Files); err != nil {
				logger.Error().Err(err).Msg("Failed to create commit")
				return err
			}
			logger.Info().Msg("Commit successfully created")
			return nil

		case "e": // edit
			editedMessage := editContent(state.Message, "COMMIT")
			// Here you might want to validate the edited message
			// and potentially regenerate if it's invalid
			state.Message = editedMessage

		case "r": // retry
			// Clear previous state to force regeneration
			generator = nil
			generator, err = commit.NewGenerator(cfg, llmClient, repo)
			if err != nil {
				return errors.Wrap(errors.CodeGitError, err)
			}

		default: // quit
			logger.Info().Msg("Commit aborted")
			return nil
		}
	}
}

// Helper function to build commit prompt
func buildCommitPrompt(state *commit.WorkflowState) string {
	var prompt strings.Builder

	// List files
	prompt.WriteString("Files to commit:\n")
	for _, file := range state.Files {
		prompt.WriteString("  " + file + "\n")
	}

	// Commit message
	prompt.WriteString("\nCommit message:\n")
	prompt.WriteString("  " + state.Message + "\n")

	// Error handling
	if state.LastError != "" {
		prompt.WriteString("\nLast error: " + state.LastError + "\n")
	}

	// Action prompt
	prompt.WriteString("\nDo you want to (c)ommit, (e)dit, (r)etry, or (Q)uit? (c/e/r/Q) ")

	return prompt.String()
}

func runVersion(ctx context.Context, cfg *config.Config, llmClient llm.Client, repo *git.Repository) error {
	bumper, err := version.NewBumper(cfg, llmClient, repo)
	if err != nil {
		return err
	}

	for {
		// Step 1: Get or analyze version change
		if bumper.GetProposedVersion() == nil {
			proposedVersion, err := bumper.AnalyzeVersionChanges(ctx)
			if err != nil {
				if errors.IsNoChanges(err) {
					logger.Info().Msg("No changes to analyze")
					return nil
				}
				if errors.IsLLMError(err) {
					logger.Warn().Err(err).Msg("Failed to analyze changes")
				}
				return err
			}

			logger.Info().
				Str("current", bumper.GetCurrentVersion()).
				Str("proposed", proposedVersion).
				Msg("Version change suggested")
		}

		// Step 2: Get current workflow state
		state, err := bumper.GetWorkflowState()
		if err != nil {
			return errors.WrapWithContext(
				errors.CodeVersionError,
				err,
				"failed to get workflow state",
			)
		}

		// Step 3: Early exit if no changes needed
		if !state.NeedsTag && !state.NeedsCommit {
			logger.Info().Msg("No version changes required")
			return nil
		}

		// Step 4: Get user decision
		prompt := buildVersionPrompt(state)
		response, err := getUserResponse(prompt)
		if err != nil {
			return errors.WrapWithContext(
				errors.CodeInputError,
				err,
				"failed to get user input",
			)
		}

		// Step 5: Handle user action
		switch response {
		case "c", "a": // commit/apply
			if err := bumper.ApplyVersionChange(ctx); err != nil {
				logger.Error().Err(err).Msg("Failed to apply version change")
				return err
			}
			return nil

		case "e": // edit
			editedVersion := editContent(state.Proposed, "VERSION")
			// Parse edited version
			parts := strings.Split(editedVersion, "-")
			baseVersion := parts[0]
			var preRelease string
			if len(parts) > 1 {
				preRelease = parts[1]
			}

			// Determine bump type based on version change
			var bumpType string
			if strings.HasPrefix(baseVersion, "0.1.0") {
				bumpType = "minor"
			} else if strings.HasPrefix(baseVersion, "0.0.2") {
				bumpType = "patch"
			}

			if _, err := bumper.ProposeVersionChange(bumpType, preRelease); err != nil {
				logger.Warn().Err(err).Msg("Invalid version format")
				continue
			}

		case "r": // retry
			bumper.ClearProposedVersion()

		default: // quit
			logger.Info().Msg("Version bump aborted")
			return nil
		}
	}
}

func buildVersionPrompt(state *version.WorkflowState) string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("\nVersion Bump Summary:\n"+
		"Current version: %s\n"+
		"Proposed version: %s\n\nProposed changes:\n",
		state.Current, state.Proposed))

	if state.NeedsTag {
		prompt.WriteString("  • Create git tag 'v" + state.Proposed + "'")
		if state.SignTag {
			prompt.WriteString(" (signed)")
		}
		prompt.WriteString("\n")
	}

	if state.NeedsCommit {
		prompt.WriteString("  • Update files and create commit")
		if state.SignCommit {
			prompt.WriteString(" (signed)")
		}
		prompt.WriteString(":\n")
		for _, file := range state.Files {
			prompt.WriteString("    - " + file + "\n")
		}
	}

	actionWord := "commit"
	actionLetter := "c"
	if !state.NeedsCommit {
		actionWord = "apply"
		actionLetter = "a"
	}

	prompt.WriteString(fmt.Sprintf("\nDo you want to %s, (e)dit, (r)etry, or (Q)uit? (%s/e/r/Q) ",
		actionWord, actionLetter))

	return prompt.String()
}
