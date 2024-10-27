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

type Generator struct {
	cfg   *config.Config
	llm   llm.Client
	repo  *git.Repository
	tools []config.Tool
}

func NewGenerator(cfg *config.Config, llmClient llm.Client, repo *git.Repository) (*Generator, error) {
	if err := validateGeneratorConfig(cfg); err != nil {
		return nil, err
	}

	return &Generator{
		cfg:   cfg,
		llm:   llmClient,
		repo:  repo,
		tools: cfg.Tools,
	}, nil
}

func (g *Generator) Generate(ctx context.Context) (string, error) {
	logger.Debug().Msg("Starting commit message generation")

	fileSummaries, err := g.getFileSummaries(ctx)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get file summaries",
		)
	}

	if len(fileSummaries) == 0 {
		return "", errors.Wrap(
			errors.CodeInvalidState,
			errors.ErrInvalidInput,
		)
	}

	diffSummary := g.generateDiffSummary(fileSummaries)

	commitMessage, err := g.generateCommitMessageFromSummary(ctx, diffSummary)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			err,
			"failed to generate commit message",
		)
	}

	return strings.TrimSuffix(commitMessage, "."), nil
}

func (g *Generator) getCurrentBranch() (string, error) {
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

func (g *Generator) findTool(name string) *config.Tool {
	tool := config.FindTool(g.tools, name)
	if tool != nil {
		logger.Debug().
			Str("tool_name", name).
			Str("system_prompt", tool.SystemPrompt).
			Str("user_prompt", tool.UserPrompt).
			Msg("Found tool configuration")
	} else {
		logger.Debug().
			Str("tool_name", name).
			Msg("Tool configuration not found")
	}
	return tool
}

func (g *Generator) shouldIgnoreFile(path string) bool {
	return g.repo.ShouldIgnoreFile(path, g.cfg.Git.Ignore, g.cfg.Git.IncludeGitignore)
}

func (g *Generator) generateDiffSummary(fileSummaries map[string]string) string {
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

func (g *Generator) generateFileSummary(ctx context.Context, path string, status git.StatusCode) (string, error) {
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

	tool := g.findTool("generate_file_summary")
	if tool == nil {
		logger.Error().
			Str("tool", "generate_file_summary").
			Msg("Required tool not found in configuration")
		return "", errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"generate_file_summary tool not found in configuration",
		)
	}

	logger.Debug().
		Interface("input", input).
		Msg("Analyzing file changes")

	summary, err := llm.CallTool(ctx, g.llm, tool, input)
	if err != nil {
		logger.Error().
			Err(err).
			Str("path", path).
			Msg("Failed to analyze changes using LLM")
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	return summary, nil
}

func (g *Generator) getFileSummaries(ctx context.Context) (map[string]string, error) {
	status, err := g.repo.Status()
	if err != nil {
		return nil, errors.Wrap(errors.CodeGitError, err)
	}

	fileSummaries := make(map[string]string)
	for path, fileStatus := range status {
		if g.shouldIgnoreFile(path) {
			continue
		}

		summary, err := g.generateFileSummary(ctx, path, fileStatus.Staging)
		if err != nil {
			return nil, errors.WrapWithContext(
				errors.CodeGitError,
				err,
				"failed to generate summary for "+path, // Removed unnecessary fmt.Sprintf
			)
		}

		fileSummaries[path] = summary
	}

	if len(fileSummaries) == 0 {
		// Return ErrInvalidInput instead of just a code
		return nil, errors.Wrap(errors.CodeInvalidState, errors.ErrInvalidInput)
	}

	return fileSummaries, nil
}

//nolint:cyclop // Complex function handling commit message generation
func (g *Generator) generateCommitMessageFromSummary(ctx context.Context, summary string) (string, error) {
	select {
	case <-ctx.Done():
		return "", errors.Wrap(errors.CodeTimeoutError, ctx.Err())
	default:
		tool := g.findTool("generate_commit_message")
		if tool == nil {
			return "", errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidConfig,
				"generate_commit_message tool not found",
			)
		}

		branchName, err := g.getCurrentBranch()
		if err != nil {
			return "", err // Already wrapped
		}

		maxRetries := g.cfg.LLM.MaxRetries
		if maxRetries < 1 {
			maxRetries = 1
		}

		input := map[string]interface{}{
			"summary": summary,
			"branch":  branchName,
		}

		var lastMessage string
		var lastError string

		for retries := 0; retries < maxRetries; retries++ {
			logger.Debug().
				Int("attempt", retries+1).
				Int("maxRetries", maxRetries).
				Msg("Attempting to generate commit message")

			// Use retry tool if this isn't the first attempt
			currentTool := tool
			if retries > 0 {
				currentTool = g.findTool("retry_commit_message")
				if currentTool == nil {
					logger.Warn().Msg("Retry tool not found, falling back to original tool")
					currentTool = tool
				} else {
					input = map[string]interface{}{
						"summary":  summary,
						"branch":   branchName,
						"previous": lastMessage,
						"error":    lastError,
					}
				}
			}

			message, err := llm.CallTool(ctx, g.llm, currentTool, input)
			if err != nil {
				return "", errors.WrapWithContext(
					errors.CodeLLMError,
					err,
					fmt.Sprintf("failed to generate message (attempt %d/%d)", retries+1, maxRetries),
				)
			}

			message = cleanCommitMessage(message)

			if g.isValidCommitMessage(message) {
				logger.Info().
					Str("message", message).
					Int("attempts", retries+1).
					Msg("Generated valid commit message")
				return message, nil
			}

			if retries < maxRetries-1 {
				lastMessage = message
				lastError = g.analyzeInvalidMessage(message)
				logger.Debug().
					Str("message", message).
					Str("reason", lastError).
					Int("attempt", retries+1).
					Msg("Invalid commit message")
			}
		}

		return "", errors.WrapWithContext(
			errors.CodeValidateError,
			errors.ErrInvalidInput,
			fmt.Sprintf("failed to generate valid commit message after %d attempts", maxRetries),
		)
	}
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

//nolint:cyclop // Complexity justified by nature of validation logic
func (g *Generator) analyzeInvalidMessage(message string) string {
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

	parts := strings.SplitN(header, ":", 2) //nolint:mnd // Split into type+scope and description
	typeAndScope := parts[0]
	description := ""
	if len(parts) > 1 {
		description = strings.TrimSpace(parts[1])
	}

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
			typeAndScope[strings.Index(typeAndScope, "("):], //nolint:gocritic // Index won't return -1 due to Contains check
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
	if !strings.HasPrefix(description, " ") || strings.HasPrefix(description, "  ") {
		return "must have exactly one space after colon"
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
					Int("line_number", i+3). //nolint:mnd // Offset for human-readable line numbers
					Str("line", line).
					Int("preferred_length", g.cfg.Git.PreferredLineLength).
					Int("actual_length", len(line)).
					Msg("Line exceeds preferred length")
			}
		}
	}

	return "unknown validation error"
}

func (*Generator) filterImportChanges(diff string) (string, bool) {
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
func (g *Generator) isValidCommitMessage(message string) bool {
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
	headerPattern := fmt.Sprintf( //nolint:perfsprint // More readable for regex pattern construction
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

	parts := strings.SplitN(header, ":", 2) //nolint:mnd // Split into type+scope and description
	if len(parts) != 2 || !strings.HasPrefix(parts[1], " ") || strings.HasPrefix(parts[1], "  ") {
		logger.Debug().
			Str("header", header).
			Msg("Invalid spacing around colon")
		return false
	}

	// Validate no uppercase in description
	description := strings.TrimSpace(parts[1])
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
	}

	// Validate body format if present
	if len(lines) > 1 {
		if len(lines) > 2 && lines[1] != "" {
			logger.Debug().Msg("Missing blank line after header")
			return false
		}

		for i, line := range lines[2:] {
			if len(line) > g.cfg.Git.PreferredLineLength {
				logger.Warn().
					Int("line_number", i+3). //nolint:mnd // Offset for human-readable line numbers
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

func validateGeneratorConfig(cfg *config.Config) error {
	if cfg == nil {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"configuration is required",
		)
	}
	if len(cfg.Tools) == 0 {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"missing required tools configuration",
		)
	}

	requiredTools := []string{"generate_file_summary", "generate_commit_message"}
	for _, tool := range requiredTools {
		if !hasToolConfig(cfg.Tools, tool) {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				"missing required tool: "+tool,
			)
		}
	}
	return nil
}

func hasToolConfig(tools []config.Tool, name string) bool {
	for i := range tools {
		if tools[i].Name == name {
			return true
		}
	}
	return false
}
