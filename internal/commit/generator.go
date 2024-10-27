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
		logger.Error().Err(err).Msg("Failed to get file summaries")
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	if len(fileSummaries) == 0 {
		return "", errors.New(errors.CodeInvalidState)
	}

	diffSummary := g.generateDiffSummary(fileSummaries)
	logger.Debug().Str("diffSummary", diffSummary).Msg("Generated diff summary")

	commitMessage, err := g.generateCommitMessageFromSummary(ctx, diffSummary)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to generate valid commit message")
		return "", err
	}

	commitMessage = strings.TrimSuffix(commitMessage, ".")

	logger.Info().Str("message", commitMessage).Msgf("Generated commit message: %s", commitMessage)

	return commitMessage, nil
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
	for file, summary := range fileSummaries {
		summaryBuilder.WriteString(fmt.Sprintf("- %s: %s\n", file, summary))
	}

	return summaryBuilder.String()
}

func (g *Generator) generateFileSummary(ctx context.Context, path string, status git.StatusCode) (string, error) {
	logger.Debug().
		Str("path", path).
		Str("status", git.GetFileStatus(status)).
		Msg("Generating file summary")

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

	tool := g.findTool("analyze_file_changes")
	if tool == nil {
		logger.Error().Msg("analyze_file_changes tool not found in configuration")
		return "", errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"analyze_file_changes tool not found",
		)
	}

	logger.Debug().
		Interface("input", input).
		Msg("Calling analyze_file_changes tool")

	summary, err := llm.CallTool(ctx, g.llm, tool, input)
	if err != nil {
		logger.Error().
			Err(err).
			Str("path", path).
			Msg("Failed to generate summary using LLM")
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	return summary, nil
}

func (g *Generator) getFileSummaries(ctx context.Context) (map[string]string, error) {
	status, err := g.repo.Status()
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get git status")
		return nil, errors.Wrap(errors.CodeGitError, err)
	}

	fileSummaries := make(map[string]string)
	for path, fileStatus := range status {
		logger.Debug().
			Str("path", path).
			Str("status", git.GetFileStatus(fileStatus.Staging)).
			Msg("Processing file")

		if g.shouldIgnoreFile(path) {
			logger.Debug().Str("path", path).Msg("Ignoring file")
			continue
		}

		summary, err := g.generateFileSummary(ctx, path, fileStatus.Staging)
		if err != nil {
			logger.Error().
				Err(err).
				Str("path", path).
				Str("status", git.GetFileStatus(fileStatus.Staging)).
				Msg("Failed to generate file summary")
			return nil, errors.Wrap(errors.CodeGitError, err)
		}

		fileSummaries[path] = summary
	}

	logger.Debug().
		Int("total_files", len(status)).
		Int("processed_files", len(fileSummaries)).
		Msg("Generated file summaries")

	if len(fileSummaries) == 0 {
		return nil, errors.WrapWithContext(
			errors.CodeInvalidState,
			errors.ErrInvalidInput,
			"no files to commit after filtering",
		)
	}

	return fileSummaries, nil
}

func (g *Generator) generateCommitMessageFromSummary(ctx context.Context, summary string) (string, error) {
	branchName, err := g.getCurrentBranch()
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get current branch name")
		branchName = "unknown"
	}

	// Initial prompt with clear instructions
	systemPrompt := `You are a commit message generator that follows Conventional Commits format.
Generate ONLY the commit message, no explanation or extra text.
Format: type(optional-scope): description
- type must be: feat, fix, docs, style, refactor, perf, test, chore, ci, build, revert
- scope is optional, lowercase, in parentheses
- description must be lowercase, no period at end
- must be under 72 characters`

	input := map[string]interface{}{
		"summary": summary,
		"branch":  branchName,
		"prompt":  systemPrompt,
		"examples": []string{
			"feat(parser): add ability to parse arrays",
			"fix: handle edge case in sorting algorithm",
			"refactor(auth): simplify login flow",
			"docs: update readme installation steps",
		},
	}

	tool := g.findTool("generate_commit_message")
	if tool == nil {
		logger.Error().Msg("generate_commit_message tool not found in configuration")
		return "", errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"generate_commit_message tool not found",
		)
	}

	maxRetries := g.cfg.LLM.MaxRetries
	if maxRetries < 1 {
		maxRetries = 1
	}

	for retries := 0; retries < maxRetries; retries++ {
		logger.Debug().
			Int("attempt", retries+1).
			Int("maxRetries", maxRetries).
			Msg("Attempting to generate commit message")

		message, err := llm.CallTool(ctx, g.llm, tool, input)
		if err != nil {
			logger.Error().
				Err(err).
				Int("attempt", retries+1).
				Msg("Failed to call LLM")
			return "", errors.Wrap(errors.CodeLLMError, err)
		}

		logger.Debug().
			Str("rawMessage", message).
			Int("attempt", retries+1).
			Msg("Received message from LLM")

		// Clean up the message
		message = cleanCommitMessage(message)

		logger.Debug().
			Str("cleanedMessage", message).
			Int("attempt", retries+1).
			Msg("Cleaned message")

		if isValidCommitMessage(message) {
			logger.Info().
				Str("message", message).
				Int("attempts", retries+1).
				Msg("Generated valid commit message")
			return message, nil
		}

		if retries < maxRetries-1 {
			// Analyze why the message was invalid
			invalidReason := analyzeInvalidMessage(message)

			logger.Debug().
				Str("message", message).
				Str("reason", invalidReason).
				Int("attempt", retries+1).
				Msg("Invalid commit message")

			// Update input with feedback for next attempt
			input["error"] = invalidReason
			input["previous"] = message
			input["summary"] = fmt.Sprintf(`Previous attempt was invalid (%s).

Branch: %s

Summary of changes:
%s

Remember:
1. ONLY output the commit message line
2. Follow format: type(scope): description
3. Type must be one of: feat, fix, docs, style, refactor, perf, test, chore, ci, build, revert
4. Description must be lowercase with no period
5. Must include colon and space after type/scope`, invalidReason, branchName, summary)
		}
	}

	return "", errors.WrapWithContext(
		errors.CodeTimeoutError,
		errors.ErrInvalidInput,
		fmt.Sprintf("failed to generate valid commit message after %d attempts", maxRetries),
	)
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

func analyzeInvalidMessage(message string) string {
	if message == "" {
		return "empty message"
	}

	// Check for basic format issues
	if !strings.Contains(message, ":") {
		return "missing colon separator"
	}

	parts := strings.SplitN(message, ":", 2)
	typeAndScope := parts[0]
	description := ""
	if len(parts) > 1 {
		description = strings.TrimSpace(parts[1])
	}

	// Check type
	validTypes := []string{"feat", "fix", "docs", "style", "refactor", "perf", "test", "chore", "ci", "build", "revert"}
	hasValidType := false
	for _, t := range validTypes {
		if strings.HasPrefix(typeAndScope, t) {
			hasValidType = true
			break
		}
	}
	if !hasValidType {
		return fmt.Sprintf("invalid type '%s'", typeAndScope)
	}

	// Check scope format if present
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
		if strings.ContainsAny(scope, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			return "scope must be lowercase"
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

func isValidCommitMessage(message string) bool {
	pattern := `^(feat|fix|docs|style|refactor|perf|test|chore|ci|build|revert)(\([a-z-]+\))?: [a-z].*[^.]$`
	matched, _ := regexp.MatchString(pattern, strings.Split(message, "\n")[0])

	// Add debug logging
	logger.Debug().
		Str("message", message).
		Bool("matched", matched).
		Str("pattern", pattern).
		Msg("Validating commit message")

	return matched
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

	requiredTools := []string{"analyze_file_changes", "generate_commit_message"}
	for _, tool := range requiredTools {
		if !hasToolConfig(cfg.Tools, tool) {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				fmt.Sprintf("missing required tool: %s", tool),
			)
		}
	}
	return nil
}

func hasToolConfig(tools []config.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func validateCommitMessage(message string) (bool, string) {
	if message == "" {
		return false, "empty message"
	}

	pattern := `^(feat|fix|docs|style|refactor|perf|test|chore|ci|build|revert)(\([a-z-]+\))?: [a-z].*[^.]$`
	matched, _ := regexp.MatchString(pattern, strings.Split(message, "\n")[0])

	if !matched {
		return false, analyzeCommitMessageError(message)
	}

	return true, ""
}

func analyzeCommitMessageError(message string) string {
	if !strings.Contains(message, ":") {
		return "missing colon separator"
	}

	parts := strings.SplitN(message, ":", 2)
	if len(parts) != 2 {
		return "invalid format"
	}

	typeAndScope := parts[0]
	description := strings.TrimSpace(parts[1])

	validTypes := []string{"feat", "fix", "docs", "style", "refactor", "perf", "test", "chore", "ci", "build", "revert"}
	if !containsAny(typeAndScope, validTypes) {
		return "invalid type"
	}

	if description == "" {
		return "missing description"
	}

	return "format error"
}

func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if strings.HasPrefix(s, sub) {
			return true
		}
	}
	return false
}
