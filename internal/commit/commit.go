package commit

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/git"
	"codeberg.org/mutker/bumpa/internal/llm"
	"codeberg.org/mutker/bumpa/internal/logger"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	// Commit message components
	validVerbs = `add|update|remove|fix|refactor|implement|improve|change|modify|delete|revert|merge`
	validTypes = `feat|fix|docs|style|refactor|perf|test|chore|ci|build`
	validScope = `[a-z][a-z0-9-]*`

	maxHeaderLength  = 72 // Maximum length of commit message header
	headerPartCount  = 2  // Number of parts in commit header split
	lineNumberOffset = 3  // Offset for human-readable line numbers
	colonWithSpace   = ": "
)

// Valid commit patterns
var commitPatterns = struct {
	typeScope   string
	description string
	header      string
}{
	// Type and scope must be lowercase
	typeScope: fmt.Sprintf(`^(%s)(\(%s\))?$`, validTypes, validScope),

	// Description can be mixed case
	description: `^[a-z]+[a-z0-9 -]*[a-z0-9]$`,

	// Type and scope lowercase, description can start with capital
	header: fmt.Sprintf(`^(%s)(\(%s\))?: [A-Z][-A-Za-z0-9 ]+[a-z0-9]$`, validTypes, validScope),
}

// WorkflowState represents the current state of commit generation
type WorkflowState struct {
	Message        string   // Generated commit message
	Files          []string // Files to be committed
	HasChanges     bool     // Whether there are changes to commit
	IsMessageValid bool     // Whether the generated message is valid
	RetryCount     int      // Number of generation retries
	LastError      string   // Last error encountered
	CanCommit      bool     // Whether commit is possible
	ManuallyEdited bool     // Whether message was manually edited
}

// Commit manages commit message generation
type Commit struct {
	cfg                *config.Config
	llm                llm.Client
	repo               *git.Repository
	lastError          error
	generatedMessage   string
	manualMessage      string
	messageGeneratedAt time.Time
}

// CommitValidationResult holds the validation state and any error message
type CommitValidationResult struct {
	Valid   bool
	Message string
}

// NewGenerator creates a new commit message generator
func NewGenerator(cfg *config.Config, llmClient llm.Client, repo *git.Repository) (*Commit, error) {
	// Validate configuration and setup
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	return &Commit{
		cfg:  cfg,
		llm:  llmClient,
		repo: repo,
	}, nil
}

// GetWorkflowState provides the current state of the commit workflow
func (g *Commit) GetWorkflowState(ctx context.Context) (*WorkflowState, error) {
	// Get files to commit
	files, err := g.repo.GetFilesToCommit()
	if err != nil {
		return nil, err
	}

	// If a manual message exists, use it
	if g.manualMessage != "" {
		isValid := g.isValidCommitMessage(g.manualMessage)
		return &WorkflowState{
			Message:        g.manualMessage,
			Files:          files,
			HasChanges:     len(files) > 0,
			IsMessageValid: isValid,
			RetryCount:     0,
			LastError:      "",
			CanCommit:      isValid && len(files) > 0,
		}, nil
	}

	// Generate message if not already generated
	var message string
	var isValid bool
	var lastError string
	var retryCount int

	if g.lastError != nil {
		lastError = g.lastError.Error()
	}

	// If no message has been generated yet, attempt to generate
	if g.generatedMessage == "" {
		message, err = g.Generate(ctx)
		if err != nil {
			lastError = err.Error()
		} else {
			g.generatedMessage = message
			g.messageGeneratedAt = time.Now()
			isValid = g.isValidCommitMessage(message)
		}
	} else {
		// Use previously generated message
		message = g.generatedMessage
		isValid = g.isValidCommitMessage(message)
	}

	return &WorkflowState{
		Message:        message,
		Files:          files,
		HasChanges:     len(files) > 0,
		IsMessageValid: isValid,
		RetryCount:     retryCount,
		LastError:      lastError,
		CanCommit:      isValid && len(files) > 0,
	}, nil
}

// GetFilesToUpdate returns paths of files that will be updated/committed
func (g *Commit) GetFilesToUpdate() ([]string, error) {
	return g.repo.GetFilesToCommit()
}

func (g *Commit) Generate(ctx context.Context) (string, error) {
	fileSummaries, err := g.getFileSummaries(ctx)
	if err != nil {
		if errors.Is(err, errors.ErrInvalidInput) {
			return "", errors.WrapWithContext(
				errors.CodeNoChanges,
				err,
				"no changes are staged for commit - use 'git add' to stage files",
			)
		}
		return "", err
	}

	if len(fileSummaries) == 0 {
		return "", errors.WrapWithContext(
			errors.CodeNoChanges,
			errors.ErrInvalidInput,
			"no changes are staged for commit - use 'git add' to stage files",
		)
	}

	logger.Info().Msgf("Analyzing changes in %d files", len(fileSummaries))

	logger.Debug().
		Interface("summaries", fileSummaries).
		Msg("File change summaries")

	diffSummary := g.generateDiffSummary(fileSummaries)
	commitMessage, err := g.getCommitMessage(ctx, diffSummary)
	if err != nil {
		return "", err
	}

	return strings.TrimSuffix(commitMessage, "."), nil
}

func (g *Commit) getCurrentBranch() (string, error) {
	head, err := g.repo.Head()
	if err != nil {
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}

	refs, err := g.repo.References()
	if err != nil {
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	var closestBranch string
	var closestCommit *object.Commit
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() {
			commit, err := g.repo.CommitObject(ref.Hash())
			if err != nil {
				logger.Warn().Err(err).Str("ref", ref.Name().String()).Msg("Failed to get commit object for reference")
				return nil
			}
			if closestCommit == nil || commit.Committer.When.After(closestCommit.Committer.When) {
				closestBranch = ref.Name().Short()
				closestCommit = commit
			}
		}

		return nil
	})
	if err != nil {
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	if closestBranch == "" {
		return "DETACHED_HEAD", nil
	}

	return closestBranch, nil
}

func (g *Commit) findFunction(name string) *config.LLMFunction {
	fn := config.FindFunction(g.cfg.Functions, name)
	if fn != nil {
		logger.Debug().
			Str("function_name", fn.Name).
			Str("system_prompt", fn.SystemPrompt).
			Str("user_prompt", fn.UserPrompt).
			Msg("Found tool configuration")
	}
	return fn
}

func (g *Commit) shouldIgnoreFile(path string) bool {
	return g.repo.ShouldIgnoreFile(path, g.cfg.Git.Ignore, g.cfg.Git.IncludeGitignore)
}

func (g *Commit) generateDiffSummary(fileSummaries map[string]string) string {
	branchName, err := g.repo.GetCurrentBranch()
	if err != nil {
		logger.Warn().Err(err).Msg("failed to get current branch name")
		branchName = "unknown"
	}

	var summaryBuilder strings.Builder
	summaryBuilder.WriteString(fmt.Sprintf("Changes on branch '%s':\n\n", branchName))

	// Group changes by type
	var fileChanges []string
	var otherChanges []string

	for file, summary := range fileSummaries {
		if strings.Contains(summary, "only formatting") ||
			strings.Contains(summary, "minor changes") ||
			strings.Contains(summary, "various fixes") {
			fileChanges = append(fileChanges, "* "+file)
		} else {
			otherChanges = append(otherChanges, "* "+file+": "+summary)
		}
	}

	// Add specific changes first
	for _, change := range otherChanges {
		summaryBuilder.WriteString(change + "\n")
	}

	// Add file list if we have files with minor changes
	if len(fileChanges) > 0 {
		if len(otherChanges) > 0 {
			summaryBuilder.WriteString("\nAdditional changes:\n")
		}
		for _, file := range fileChanges {
			summaryBuilder.WriteString(file + "\n")
		}
	}

	return summaryBuilder.String()
}

func (g *Commit) getFileSummary(ctx context.Context, path string, status git.StatusCode) (string, error) {
	logger.Debug().
		Str("path", path).
		Str("status", git.GetFileStatus(status)).
		Msg("Analyzing file changes")

	diff, err := g.repo.GetFileDiff(path)
	if err != nil {
		logger.Error().
			Err(err).
			Str("path", path).
			Msg("Failed to get file diff")
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	filteredDiff, hasSignificantChanges := g.filterImportChanges(diff)

	input := map[string]interface{}{
		"file":                  path,
		"status":                git.GetFileStatus(status),
		"diff":                  filteredDiff,
		"hasSignificantChanges": hasSignificantChanges,
	}

	tool := g.findFunction("generate_file_summary")
	if tool == nil {
		logger.Error().
			Str("function", "generate_file_summary").
			Msg("Required function not found in configuration")
		return "", errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"generate_file_summary function not found in configuration",
		)
	}

	logger.Debug().
		Interface("input", input).
		Msg("Analyzing file changes")

	summary, err := llm.CallFunction(ctx, g.llm, tool, input)
	if err != nil {
		logger.Error().
			Err(err).
			Str("path", path).
			Msg("Failed to analyze changes using LLM")
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	return summary, nil
}

func (g *Commit) getFileSummaries(ctx context.Context) (map[string]string, error) {
	status, err := g.repo.Status()
	if err != nil {
		return nil, errors.Wrap(errors.CodeGitError, err)
	}

	fileSummaries := make(map[string]string)
	for path, fileStatus := range status {
		if g.shouldIgnoreFile(path) {
			continue
		}

		summary, err := g.getFileSummary(ctx, path, fileStatus.Staging)
		if err != nil {
			return nil, errors.WrapWithContext(
				errors.CodeGitError,
				err,
				"failed to generate summary for "+path,
			)
		}

		fileSummaries[path] = summary
	}

	if len(fileSummaries) == 0 {
		return nil, errors.WrapWithContext(
			errors.CodeNoChanges,
			errors.ErrInvalidInput,
			"no changes are staged for commit, use 'git add' to stage files",
		)
	}

	return fileSummaries, nil
}

func (g *Commit) getCommitMessage(ctx context.Context, summary string) (string, error) {
	select {
	case <-ctx.Done():
		return "", errors.Wrap(errors.CodeTimeoutError, ctx.Err())
	default:
		function := g.findFunction("generate_commit_message")
		if function == nil {
			return "", errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidConfig,
				"generate_commit_message function not found",
			)
		}

		branchName, err := g.getCurrentBranch()
		if err != nil {
			return "", err
		}

		maxRetries := g.cfg.LLM.MaxRetries
		if maxRetries < 1 {
			maxRetries = 1
		}

		var lastMessage string
		var lastError string

		for retries := 0; retries < maxRetries; retries++ {
			if retries == 0 {
				logger.Info().Msg("Generating commit message")
			} else {
				logger.Info().
					Int("attempt", retries+1).
					Int("max_retries", maxRetries).
					Str("error", lastError).
					Msg("Retrying commit message generation")
			}

			// Use retry function if this isn't the first attempt
			currentFunction := function
			input := map[string]interface{}{
				"summary": summary,
				"branch":  branchName,
			}

			if retries > 0 {
				currentFunction = g.findFunction("retry_commit_message")
				if currentFunction == nil {
					logger.Debug().Msg("Retry function not found, using original function")
					currentFunction = function
				}
				input["previous"] = lastMessage
				input["error"] = lastError
			}

			message, err := llm.CallFunction(ctx, g.llm, currentFunction, input)
			if err != nil {
				logger.Debug().
					Err(err).
					Int("attempt", retries+1).
					Msg("Failed to generate message")
				continue
			}

			message = cleanCommitMessage(message)

			// INFO log for the proposed commit message
			logger.Info().
				Str("proposed_message", message).
				Int("attempt", retries+1).
				Msg("Proposed commit message")

			if invalid := g.analyzeInvalidMessage(message); invalid != "" {
				lastMessage = message
				lastError = invalid

				logger.Error().
					Str("message", message).
					Str("error", lastError).
					Int("attempt", retries+1).
					Msg("Invalid commit message")

				continue
			}

			return message, nil
		}

		// Final error after all retries
		logger.Info().
			Msg("The LLM is struggling to generate a valid commit message. " +
				"Try running the command again, make the changes smaller, or commit manually")

		logger.Warn().
			Int("max_retries", maxRetries).
			Str("reason", lastError).
			Msg("Failed to generate valid commit message after retries")

		return "", errors.WrapWithContext(
			errors.CodeLLMGenFailed,
			errors.ErrInvalidInput,
			"failed to generate commit message: "+lastError,
		)
	}
}

// ValidateCommitMessage handles all commit message validation with detailed feedback
func (g *Commit) ValidateCommitMessage(message string) CommitValidationResult {
	if message == "" {
		return CommitValidationResult{Valid: false, Message: "empty message"}
	}

	lines := strings.Split(message, "\n")
	header := lines[0]

	if len(header) > maxHeaderLength {
		return CommitValidationResult{
			Valid:   false,
			Message: fmt.Sprintf("header too long (%d chars, max %d)", len(header), maxHeaderLength),
		}
	}

	parts := strings.SplitN(header, ":", headerPartCount)
	if len(parts) != headerPartCount {
		return CommitValidationResult{Valid: false, Message: "missing colon separator"}
	}

	typeAndScope := strings.TrimSpace(parts[0])
	description := strings.TrimSpace(parts[1])

	// Space after colon validation
	if !strings.HasPrefix(parts[1], " ") || strings.HasPrefix(parts[1], "  ") {
		return CommitValidationResult{Valid: false, Message: "must have exactly one space after colon"}
	}

	// Type and scope validation
	if !regexp.MustCompile(commitPatterns.typeScope).MatchString(typeAndScope) {
		return CommitValidationResult{
			Valid:   false,
			Message: fmt.Sprintf("invalid type or scope format in '%s'", typeAndScope),
		}
	}

	// Description validation
	if strings.HasSuffix(description, ".") {
		return CommitValidationResult{Valid: false, Message: "description ends with period"}
	}

	// Explicit verb and description validation
	descriptionWords := strings.Fields(description)
	if len(descriptionWords) == 0 {
		return CommitValidationResult{Valid: false, Message: "description is empty"}
	}

	// Check first word is a valid verb (case-sensitive)
	firstWord := descriptionWords[0]
	validVerbsList := []string{
		"add", "update", "remove", "fix", "refactor",
		"implement", "improve", "change", "modify",
		"delete", "revert", "merge",
	}

	verbFound := false
	for _, verb := range validVerbsList {
		if firstWord == verb {
			verbFound = true
			break
		}
	}

	if !verbFound {
		return CommitValidationResult{
			Valid: false,
			Message: "description must start with a valid verb: " +
				strings.Join(validVerbsList, ", "),
		}
	}

	// Detailed description validation
	if !regexp.MustCompile(commitPatterns.description).MatchString(description) {
		return CommitValidationResult{
			Valid:   false,
			Message: "description must contain only lowercase letters, numbers, spaces, and hyphens",
		}
	}

	// Body validation
	if len(lines) > 1 {
		if len(lines) > 2 && lines[1] != "" {
			return CommitValidationResult{Valid: false, Message: "must have blank line after header"}
		}

		for i, line := range lines[2:] {
			if len(line) > g.cfg.Git.PreferredLineLength {
				return CommitValidationResult{
					Valid:   false,
					Message: fmt.Sprintf("line %d exceeds preferred length", i+lineNumberOffset),
				}
			}
		}
	}

	return CommitValidationResult{Valid: true}
}

func (g *Commit) analyzeInvalidMessage(message string) string {
	result := g.ValidateCommitMessage(message)
	return result.Message
}

func (*Commit) filterImportChanges(diff string) (string, bool) {
	lines := strings.Split(diff, "\n")
	var filteredLines []string
	inImportBlock := false
	significantChanges := false

	for _, line := range lines {
		if strings.HasPrefix(line, "import (") {
			inImportBlock = true
		} else if inImportBlock && strings.HasPrefix(line, ")") {
			inImportBlock = false
		}

		if inImportBlock {
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
				filteredLines = append(filteredLines, line)
				significantChanges = true
			}
		} else {
			filteredLines = append(filteredLines, line)
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
				significantChanges = true
			}
		}
	}

	return strings.Join(filteredLines, "\n"), significantChanges
}

func (g *Commit) isValidCommitMessage(message string) bool {
	result := g.ValidateCommitMessage(message)
	if !result.Valid {
		logger.Warn().
			Str("message", message).
			Str("error", result.Message).
			Msg("Invalid commit message")
	}
	return result.Valid
}

func (g *Commit) SetManualMessage(message string) {
	g.manualMessage = strings.TrimSpace(message)
}

func (g *Commit) ClearManualMessage() {
	g.manualMessage = ""
}

func cleanCommitMessage(message string) string {
	// Remove any markdown formatting
	message = strings.ReplaceAll(message, "`", "")
	message = strings.ReplaceAll(message, "\"", "")

	// Get first line only
	if idx := strings.Index(message, "\n"); idx != -1 {
		message = message[:idx]
	}

	// Remove common prefixes LLMs might add
	prefixes := []string{
		"Here's a commit message:",
		"Commit message:",
		"Generated commit message:",
		"The commit message is:",
	}
	for _, prefix := range prefixes {
		message = strings.TrimPrefix(message, prefix)
	}

	// Clean up whitespace and periods
	message = strings.TrimSpace(message)
	message = strings.TrimSuffix(message, ".")

	return message
}

func validateConfig(cfg *config.Config) error {
	if cfg == nil {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"configuration is required",
		)
	}
	if len(cfg.Functions) == 0 {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"missing required functions configuration",
		)
	}

	requiredFunctions := []string{"generate_file_summary", "generate_commit_message"}
	for _, function := range requiredFunctions {
		if !hasFunctionConfig(cfg.Functions, function) {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				"missing required function: "+function,
			)
		}
	}
	return nil
}

func hasFunctionConfig(functions []config.LLMFunction, name string) bool {
	for i := range functions {
		if functions[i].Name == name {
			return true
		}
	}
	return false
}
