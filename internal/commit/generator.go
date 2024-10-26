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

func NewGenerator(cfg *config.Config, llmClient llm.Client, repo *git.Repository) *Generator {
	return &Generator{
		cfg:   cfg,
		llm:   llmClient,
		repo:  repo,
		tools: cfg.Tools,
	}
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

func (g *Generator) findTool(name string) *config.Tool {
	return config.FindTool(g.tools, name)
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

	summary, err := llm.CallTool(ctx, g.llm, tool.Name, input)
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

	input := map[string]interface{}{
		"summary": summary,
		"branch":  branchName,
	}

	tool := g.findTool("generate_conventional_commit")
	if tool == nil {
		return "", errors.New(errors.CodeConfigError)
	}

	maxRetries := g.cfg.LLM.MaxRetries
	if maxRetries < 1 {
		maxRetries = 1
	}

	for retries := 0; retries < maxRetries; retries++ {
		message, err := llm.CallTool(ctx, g.llm, tool.Name, input)
		if err != nil {
			return "", errors.Wrap(errors.CodeLLMError, err)
		}

		logger.Debug().Str("generatedMessage", message).Msg("Received message from LLM")

		message = strings.TrimSuffix(message, ".")

		if isValidCommitMessage(message) {
			return message, nil
		}

		if retries < maxRetries-1 {
			input["summary"] = fmt.Sprintf("The previous response was invalid. "+
				"Please generate a commit message in the Conventional Commits format. "+
				"It should start with a type (feat, fix, refactor, etc.) followed by a colon and a short description. "+
				"Do not end with a period. Consider the branch name '%s'. Here's the summary again:\n\n%s",
				branchName, summary)
		}
	}

	return "", errors.New(errors.CodeTimeoutError)
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
	return matched
}
