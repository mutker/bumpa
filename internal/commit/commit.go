package commit

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/git"
	"codeberg.org/mutker/bumpa/internal/llm"
	"codeberg.org/mutker/bumpa/internal/logger"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	maxHeaderLength = 72 // Maximum length of commit message header
)

// Valid commit types
var validTypes = []string{
	"feat", "fix", "docs", "style", "refactor",
	"perf", "test", "chore", "ci", "build",
}

// WorkflowState represents the current state of commit generation
type WorkflowState struct {
	Message        string   // Generated commit message
	Files          []string // Files to be committed
	HasChanges     bool     // Whether there are changes to commit
	IsMessageValid bool     // Whether the generated message is valid
	RetryCount     int      // Number of generation attempts
	LastError      string   // Last error encountered
	CanCommit      bool     // Whether commit is possible
}

// Commit manages commit message generation
type Commit struct {
	cfg       *config.Config
	llm       llm.Client
	repo      *git.Repository
	files     []string
	lastError error
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

	// Generate message if not already generated
	var message string
	var isValid bool
	var lastError string
	var retryCount int // Initialize retry count

	if g.lastError != nil {
		lastError = g.lastError.Error()
	}

	// If no message has been generated yet, attempt to generate
	if message == "" {
		message, err = g.Generate(ctx)
		if err != nil {
			lastError = err.Error()
		} else {
			isValid = g.isValidCommitMessage(message)
		}
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
			"no changes are staged for commit - use 'git add' to stage files",
		)
	}

	return fileSummaries, nil
}

//nolint:cyclop // Complex function handling commit message generation
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

				logger.Debug().
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

//nolint:cyclop // Complexity justified by nature of validation logic
func (g *Commit) analyzeInvalidMessage(message string) string {
	if message == "" {
		return "empty message"
	}

	lines := strings.Split(message, "\n")
	header := lines[0]

	// Check length
	if len(header) > maxHeaderLength {
		return fmt.Sprintf("header too long (%d chars, max %d)", len(header), maxHeaderLength)
	}

	// Check for colon
	if !strings.Contains(header, ":") {
		return "missing colon separator"
	}

	parts := strings.SplitN(header, ":", 2)
	typeAndScope := parts[0]
	description := ""
	if len(parts) > 1 {
		description = parts[1] // Include full description
	}

	// Check spacing after colon
	if len(description) == 0 || description[0] != ' ' {
		return "must have exactly one space after colon"
	}
	description = strings.TrimSpace(description)

	// Check type
	validTypes := []string{
		"feat", "fix", "docs", "style", "refactor",
		"perf", "test", "chore", "ci", "build",
	}
	hasValidType := false
	for _, t := range validTypes {
		if strings.HasPrefix(typeAndScope, t) {
			hasValidType = true
			break
		}
	}
	if !hasValidType {
		return fmt.Sprintf("invalid type '%s', must be one of: %s",
			typeAndScope, strings.Join(validTypes, ", "))
	}

	// Check scope format
	if strings.Contains(typeAndScope, "(") {
		if !strings.HasSuffix(typeAndScope, ")") {
			return "malformed scope - missing closing parenthesis"
		}
		scope := strings.TrimSuffix(strings.TrimPrefix(
			typeAndScope[strings.Index(typeAndScope, "("):],
			"(",
		), ")")
		if scope == "" {
			return "empty scope"
		}
		if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(scope) {
			return "scope must be lowercase and may only contain letters, numbers, and hyphens"
		}
	}

	// Check description
	if description == "" {
		return "missing description"
	}
	if strings.HasSuffix(description, ".") {
		return "description ends with period"
	}
	if strings.ToLower(description) != description {
		return "description must be lowercase"
	}
	if !regexp.MustCompile(`^[a-z][-a-z0-9 ]*[a-z0-9]$`).MatchString(description) {
		return "description must start with lowercase letter, end with letter/number, " +
			"and contain only lowercase letters, numbers, spaces, and hyphens"
	}

	// Check body format
	if len(lines) > 1 {
		if len(lines) > 2 && lines[1] != "" {
			return "must have blank line after header"
		}
		for i, line := range lines[2:] {
			if len(line) > g.cfg.Git.PreferredLineLength {
				logger.Warn().
					Int("line_number", i+3).
					Str("line", line).
					Int("preferred_length", g.cfg.Git.PreferredLineLength).
					Int("actual_length", len(line)).
					Msg("Line exceeds preferred length")
			}
		}
	}

	return ""
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

//nolint:cyclop // Complex function handling multiple validation steps
func (g *Commit) isValidCommitMessage(message string) bool {
	lines := strings.Split(message, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return false
	}

	// Header validation
	header := lines[0]
	if len(header) > maxHeaderLength {
		logger.Debug().
			Str("header", header).
			Msg("Header exceeds maximum length")
		return false
	}

	// Check type(scope): description format
	typeMatch := fmt.Sprintf(`^(%s)`, strings.Join(validTypes, "|"))
	headerPattern := fmt.Sprintf(
		`%s(\([a-z][a-z0-9-]*\))?: [a-z][-a-z0-9 ]*[a-z0-9]$`,
		typeMatch,
	)
	matched, err := regexp.MatchString(headerPattern, header)
	if err != nil || !matched {
		logger.Debug().
			Str("header", header).
			Str("pattern", headerPattern).
			Bool("matched", matched).
			Msg("Header format invalid")
		return false
	}

	// More precise colon and space validation
	parts := strings.SplitN(header, ":", 2)
	if len(parts) != 2 {
		logger.Debug().
			Str("header", header).
			Msg("Missing colon")
		return false
	}

	description := parts[1]
	if len(description) == 0 || description[0] != ' ' || strings.HasPrefix(description, "  ") {
		logger.Debug().
			Str("header", header).
			Str("description", description).
			Msg("Invalid spacing around colon")
		return false
	}

	// Trim and validate description
	description = strings.TrimSpace(description)
	if strings.ToLower(description) != description {
		logger.Debug().
			Str("description", description).
			Msg("Description contains uppercase characters")
		return false
	}

	// Validate no period at end
	if strings.HasSuffix(description, ".") {
		logger.Debug().
			Str("description", description).
			Msg("Description ends with period")
		return false
	}

	// Validate body format if present
	if len(lines) > 1 {
		// Must have blank line after header
		if len(lines) > 2 && lines[1] != "" {
			logger.Debug().Msg("Missing blank line after header")
			return false
		}

		for i, line := range lines[2:] {
			if len(line) > g.cfg.Git.PreferredLineLength {
				logger.Warn().
					Int("line_number", i+3).
					Str("line", line).
					Int("preferred_length", g.cfg.Git.PreferredLineLength).
					Int("actual_length", len(line)).
					Msg("Line exceeds preferred length")
			}
		}
	}

	logger.Debug().
		Str("header", header).
		Int("lines", len(lines)).
		Msg("Valid commit message")

	return true
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
