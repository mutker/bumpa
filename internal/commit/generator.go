package commit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/llm"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Generator struct {
	cfg    *config.Config
	llm    llm.Client
	logger zerolog.Logger
	repo   *git.Repository
}

func NewGenerator(cfg *config.Config, llm llm.Client, logger zerolog.Logger, repo *git.Repository) *Generator {
	return &Generator{cfg: cfg, llm: llm, logger: logger, repo: repo}
}

func (g *Generator) Generate() (string, error) {
	fileSummaries, err := g.getFileSummaries()
	if err != nil {
		return "", fmt.Errorf("failed to get file summaries: %w", err)
	}

	if len(fileSummaries) == 0 {
		return "", fmt.Errorf("no changes to commit")
	}

	diffSummary := g.generateDiffSummary(fileSummaries)
	log.Debug().Str("diffSummary", diffSummary).Msg("Generated diff summary")

	commitMessage, err := g.generateCommitMessageFromSummary(diffSummary)
	if err != nil {
		log.Error().Err(err).Msg("Failed to generate valid commit message")
		return "", err
	}

	// Remove trailing period if present
	commitMessage = strings.TrimSuffix(commitMessage, ".")

	log.Info().Str("message", commitMessage).Msgf("Generated commit message: %s", commitMessage)
	return commitMessage, nil
}

func (g *Generator) findTool(name string) *config.Tool {
	for _, tool := range g.cfg.Tools {
		if tool.Name == name {
			return &tool
		}
	}
	return nil
}

func (g *Generator) shouldIgnoreFile(path string) bool {
	// Check against the ignore list in the config
	for _, pattern := range g.cfg.Git.Ignore {
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}

	// Check against .gitignore if IncludeGitignore is true
	if g.cfg.Git.IncludeGitignore {
		wt, err := g.repo.Worktree()
		if err != nil {
			log.Error().Err(err).Msg("Failed to get worktree")
			return false
		}

		patterns, err := gitignore.ReadPatterns(wt.Filesystem, nil)
		if err != nil {
			log.Error().Err(err).Msg("Failed to read gitignore patterns")
			return false
		}

		matcher := gitignore.NewMatcher(patterns)
		if matcher.Match([]string{path}, false) {
			return true
		}
	}

	return false
}

func (g *Generator) generateDiffSummary(fileSummaries map[string]string) string {
	branchName, err := g.getCurrentBranch()
	if err != nil {
		log.Warn().Err(err).Msg("failed to get current branch name")
		branchName = "unknown"
	}

	var summaryBuilder strings.Builder
	summaryBuilder.WriteString(fmt.Sprintf("Changes on branch '%s':\n\n", branchName))
	for file, summary := range fileSummaries {
		summaryBuilder.WriteString(fmt.Sprintf("- %s: %s\n", file, summary))
	}
	return summaryBuilder.String()
}

func (g *Generator) getFileStatus(status git.StatusCode) string {
	switch status {
	case git.Added:
		return "Added"
	case git.Modified:
		return "Modified"
	case git.Deleted:
		return "Deleted"
	case git.Untracked:
		return "Untracked"
	default:
		return "Changed"
	}
}

func (g *Generator) getCurrentBranch() (string, error) {
	head, err := g.repo.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD: %w", err)
	}

	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}

	// If HEAD is detached, try to find the closest branch
	refs, err := g.repo.References()
	if err != nil {
		return "", fmt.Errorf("failed to get references: %w", err)
	}

	var closestBranch string
	var closestCommit *object.Commit
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() {
			commit, err := g.repo.CommitObject(ref.Hash())
			if err != nil {
				return nil // Skip this reference
			}
			if closestCommit == nil || commit.Committer.When.After(closestCommit.Committer.When) {
				closestBranch = ref.Name().Short()
				closestCommit = commit
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to iterate over references: %w", err)
	}

	if closestBranch == "" {
		return "DETACHED_HEAD", nil
	}

	return closestBranch, nil
}

func (g *Generator) getFileDiff(path string) (string, error) {
	w, err := g.repo.Worktree()
	if err != nil {
		return "", err
	}

	status, err := w.Status()
	if err != nil {
		return "", err
	}

	fileStatus := status.File(path)
	if fileStatus.Staging == git.Untracked {
		return "[New File]", nil
	}

	if fileStatus.Staging == git.Deleted {
		return "[Deleted File]", nil
	}

	currentContent, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	head, err := g.repo.Head()
	if err != nil {
		return "", err
	}

	commit, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		return "", err
	}

	file, err := commit.File(path)
	if err != nil {
		if err == object.ErrFileNotFound {
			return "[New File]", nil
		}
		return "", err
	}

	oldContent, err := file.Contents()
	if err != nil {
		return "", err
	}

	diff := g.generateSimpleDiff(oldContent, string(currentContent))
	return g.summarizeDiff(diff), nil
}

func (g *Generator) generateSimpleDiff(old, new string) string {
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new, "\n")

	var diff strings.Builder
	for i := 0; i < len(oldLines) || i < len(newLines); i++ {
		if i < len(oldLines) && i < len(newLines) && oldLines[i] == newLines[i] {
			continue
		}
		if i < len(oldLines) {
			diff.WriteString("- " + oldLines[i] + "\n")
		}
		if i < len(newLines) {
			diff.WriteString("+ " + newLines[i] + "\n")
		}
	}
	return diff.String()
}

func (g *Generator) summarizeDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	if len(lines) > 10 {
		return strings.Join(lines[:10], "\n") + "\n..."
	}
	return diff
}

func (g *Generator) generateFileSummary(path string, status git.StatusCode) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	diff, err := g.getFileDiff(path)
	if err != nil {
		return "", fmt.Errorf("failed to get diff for %s: %w", path, err)
	}

	filteredDiff, hasSignificantChanges := g.filterImportChanges(diff)

	input := map[string]interface{}{
		"file":                  path,
		"status":                g.getFileStatus(status),
		"diff":                  filteredDiff,
		"hasSignificantChanges": hasSignificantChanges,
	}

	tool := g.findTool("generate_file_summary")
	if tool == nil {
		return "", fmt.Errorf("generate_file_summary tool not found in configuration")
	}

	summary, err := llm.CallTool(ctx, g.llm, tool.Name, input)
	if err != nil {
		return "", fmt.Errorf("failed to generate file summary: %w", err)
	}

	log.Debug().Msg(summary)

	return fmt.Sprintf("%s: %s", path, summary), nil
}

func (g *Generator) getFileSummaries() (map[string]string, error) {
	w, err := g.repo.Worktree()
	if err != nil {
		return nil, err
	}

	status, err := w.Status()
	if err != nil {
		return nil, err
	}

	fileSummaries := make(map[string]string)
	for path, fileStatus := range status {
		if g.shouldIgnoreFile(path) {
			log.Debug().Str("path", path).Msg("Ignoring file")
			continue
		}

		summary, err := g.generateFileSummary(path, git.StatusCode(fileStatus.Staging))
		if err != nil {
			return nil, fmt.Errorf("failed to generate summary for file %s: %w", path, err)
		}
		fileSummaries[path] = summary
	}

	log.Debug().Int("count", len(fileSummaries)).Msg("Generated file summaries")
	return fileSummaries, nil
}

func (g *Generator) generateCommitMessageFromSummary(summary string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	branchName, err := g.getCurrentBranch()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get current branch name")
		branchName = "unknown"
	}

	input := map[string]interface{}{
		"summary": summary,
		"branch":  branchName,
	}

	tool := g.findTool("generate_conventional_commit")
	if tool == nil {
		return "", fmt.Errorf("generate_conventional_commit tool not found in configuration")
	}

	maxRetries := g.cfg.LLM.MaxRetries
	if maxRetries < 1 {
		maxRetries = 1 // Ensure at least one attempt is made
	}

	for retries := 0; retries < maxRetries; retries++ {
		message, err := llm.CallTool(ctx, g.llm, tool.Name, input)
		if err != nil {
			return "", fmt.Errorf("failed to generate commit message: %w", err)
		}

		log.Debug().Str("generatedMessage", message).Msg("Received message from LLM")

		// Remove trailing period if present
		message = strings.TrimSuffix(message, ".")

		if isValidCommitMessage(message) {
			return message, nil
		}

		// If invalid and not the last attempt, update the input and try again
		if retries < maxRetries-1 {
			input["summary"] = fmt.Sprintf("The previous response was invalid. Please generate a commit message in the Conventional Commits format. It should start with a type (feat, fix, refactor, etc.) followed by a colon and a short description. Do not end with a period. Consider the branch name '%s'. Here's the summary again:\n\n%s", branchName, summary)
		}
	}

	return "", fmt.Errorf("failed to generate a valid commit message after %d attempts", maxRetries)
}

func (g *Generator) getFilesToCommit() ([]string, error) {
	w, err := g.repo.Worktree()
	if err != nil {
		return nil, err
	}

	status, err := w.Status()
	if err != nil {
		return nil, err
	}

	var files []string
	for file, fileStatus := range status {
		if fileStatus.Staging != git.Unmodified || fileStatus.Worktree != git.Unmodified {
			files = append(files, file)
		}
	}

	sort.Strings(files)
	return files, nil
}

func (g *Generator) makeCommit(message string, filesToAdd []string) error {
	w, err := g.repo.Worktree()
	if err != nil {
		return err
	}

	for _, file := range filesToAdd {
		_, err := w.Add(file)
		if err != nil {
			return fmt.Errorf("failed to add file %s: %w", file, err)
		}
	}

	cfg, err := g.repo.Config()
	if err != nil {
		return fmt.Errorf("failed to get git config: %w", err)
	}

	name := cfg.User.Name
	email := cfg.User.Email

	if name == "" || email == "" {
		return fmt.Errorf("git user name or email not set in .git/config")
	}

	commitOptions := &git.CommitOptions{
		Author: &object.Signature{
			Name:  name,
			Email: email,
			When:  time.Now(),
		},
		All: true,
	}

	_, err = w.Commit(message, commitOptions)
	return err
}

func (g *Generator) filterImportChanges(diff string) (string, bool) {
	lines := strings.Split(diff, "\n")
	var filteredLines []string
	var nonImportLines []string
	inImportBlock := false
	significantImportChanges := false
	nonImportChanges := 0

	for _, line := range lines {
		if strings.HasPrefix(line, "import (") {
			inImportBlock = true
			filteredLines = append(filteredLines, line)
			continue
		}
		if inImportBlock && strings.HasPrefix(line, ")") {
			inImportBlock = false
			filteredLines = append(filteredLines, line)
			continue
		}
		if inImportBlock {
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
				significantImportChanges = true
				filteredLines = append(filteredLines, line)
			}
		} else {
			nonImportLines = append(nonImportLines, line)
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
				nonImportChanges++
			}
		}
	}

	filteredLines = append(filteredLines, nonImportLines...)
	return strings.Join(filteredLines, "\n"), nonImportChanges > 0 || significantImportChanges
}

func isValidCommitMessage(message string) bool {
	pattern := `^(feat|fix|docs|style|refactor|perf|test|chore|ci|build|revert)(\([a-z-]+\))?: [a-z].*[^.]$`
	matched, _ := regexp.MatchString(pattern, strings.Split(message, "\n")[0])
	return matched
}
