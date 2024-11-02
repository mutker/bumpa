//nolint:wrapcheck // Using our own error wrapping system throughout package
package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/logger"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

type Repository struct {
	repo *gogit.Repository
	cfg  config.GitConfig
}

type StatusCode = gogit.StatusCode

const (
	Unmodified         = gogit.Unmodified
	Untracked          = gogit.Untracked
	Modified           = gogit.Modified
	Added              = gogit.Added
	Deleted            = gogit.Deleted
	Renamed            = gogit.Renamed
	Copied             = gogit.Copied
	UpdatedButUnmerged = gogit.UpdatedButUnmerged
	newFileMessage     = "[New File]"
	deletedFileMessage = "[Deleted File]"
)

func OpenRepository(path string, cfg config.GitConfig) (*Repository, error) {
	repo, err := gogit.PlainOpen(path)
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitRepoOpen,
		)
	}
	return &Repository{repo: repo, cfg: cfg}, nil
}

func (r *Repository) Head() (*plumbing.Reference, error) {
	head, err := r.repo.Head()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitBranch,
		)
	}
	return head, nil
}

//nolint:ireturn // Interface return needed for go-git compatibility
func (r *Repository) References() (storer.ReferenceIter, error) {
	refs, err := r.repo.References()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get repository references",
		)
	}
	return refs, nil
}

func (r *Repository) CommitObject(hash plumbing.Hash) (*object.Commit, error) {
	commit, err := r.repo.CommitObject(hash)
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get commit object",
		)
	}
	return commit, nil
}

func (r *Repository) GetCurrentBranch() (string, error) {
	head, err := r.Head()
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitBranch,
		)
	}

	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}

	refs, err := r.repo.References()
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get repository references",
		)
	}

	var closestBranch string
	var closestCommit *object.Commit
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() {
			commit, err := r.CommitObject(ref.Hash())
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
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to iterate references",
		)
	}

	if closestBranch == "" {
		return "DETACHED_HEAD", nil
	}

	return closestBranch, nil
}

//nolint:cyclop // Complex but necessary function handling multiple git operations
func (r *Repository) GetFileDiff(path string) (string, error) {
	w, err := r.repo.Worktree()
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitWorkTree,
		)
	}

	status, err := w.Status()
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitStatus,
		)
	}

	fileStatus := status.File(path)
	if fileStatus.Staging == Untracked {
		return newFileMessage, nil
	}
	if fileStatus.Staging == Deleted {
		return deletedFileMessage, nil
	}

	// Read current content
	currentContent, err := os.ReadFile(path)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.FormatContext(errors.ContextFileRead, path),
		)
	}

	// Get old content
	head, err := r.Head()
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitBranch,
		)
	}

	commit, err := r.CommitObject(head.Hash())
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get commit object",
		)
	}

	file, err := commit.File(path)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return newFileMessage, nil
		}
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitDiff,
		)
	}

	oldContent, err := file.Contents()
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitDiff,
		)
	}

	// Generate and truncate diff if needed
	diff := r.generateDiff(oldContent, string(currentContent))
	if len(strings.Split(diff, "\n")) > r.cfg.MaxDiffLines {
		diff = strings.Join(strings.Split(diff, "\n")[:r.cfg.MaxDiffLines], "\n") + "\n..."
	}

	return diff, nil
}

func (r *Repository) GetFilesToCommit() ([]string, error) {
	logger.Debug().Msg("Getting files to commit")

	w, err := r.repo.Worktree()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitWorkTree,
		)
	}

	status, err := w.Status()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitStatus,
		)
	}

	var files []string
	for file, fileStatus := range status {
		if fileStatus.Staging != Unmodified || fileStatus.Worktree != Unmodified {
			files = append(files, file)
		}
	}

	if len(files) == 0 {
		logger.Info().Msg("No changes to commit")
		return nil, errors.WrapWithContext(
			errors.CodeNoChanges,
			errors.ErrInvalidInput,
			errors.ContextNoChanges,
		)
	}

	logger.Debug().Int("fileCount", len(files)).Msg("Files to commit")
	return files, nil
}

// getUserConfig returns the user's name and email from git config.
//
//nolint:nonamedreturns,cyclop // Using named returns for clarity as recommended by gocritic
func (r *Repository) getUserConfig() (name, email string, err error) {
	// With includeIf support, we should first try to get the effective config values
	// directly from git, letting it handle all the config resolution
	if isGitAvailable() {
		name, err = getConfigValue("", "user.name")
		if err != nil {
			return "", "", err
		}

		email, err = getConfigValue("", "user.email")
		if err != nil {
			return "", "", err
		}

		if name != "" && email != "" {
			return name, email, nil
		}
	}

	// Fall back to repo config only if git isn't available or values weren't found
	cfg, err := r.repo.Config()
	if err != nil {
		return "", "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get git config",
		)
	}

	if name == "" {
		name = cfg.User.Name
	}
	if email == "" {
		email = cfg.User.Email
	}

	// Validate user information
	if name == "" || email == "" {
		return "", "", errors.WrapWithContext(
			errors.CodeGitError,
			errors.ErrInvalidInput,
			"git user not configured - ensure your git config includes appropriate user settings",
		)
	}

	return name, email, nil
}

// stageFiles stages the given files in the worktree
func stageFiles(w *gogit.Worktree, files []string) error {
	for _, file := range files {
		_, err := w.Add(file)
		if err != nil {
			return errors.WrapWithContext(
				errors.CodeGitError,
				err,
				"failed to stage file: "+file,
			)
		}
	}
	return nil
}

// MakeCommit creates a new commit with the given message and files
func (r *Repository) MakeCommit(ctx context.Context, message string, filesToAdd []string) error {
	select {
	case <-ctx.Done():
		return errors.Wrap(errors.CodeTimeoutError, ctx.Err())
	default:
		// Get worktree
		w, err := r.repo.Worktree()
		if err != nil {
			return errors.WrapWithContext(
				errors.CodeGitError,
				err,
				errors.ContextGitWorkTree,
			)
		}

		// Stage files
		if err := stageFiles(w, filesToAdd); err != nil {
			return err
		}

		// Get user configuration
		name, email, err := r.getUserConfig()
		if err != nil {
			return err
		}

		// Create commit
		_, err = w.Commit(message, &gogit.CommitOptions{
			Author: &object.Signature{
				Name:  name,
				Email: email,
				When:  time.Now(),
			},
			All: true,
		})
		if err != nil {
			return errors.WrapWithContext(
				errors.CodeGitError,
				err,
				errors.ContextGitCommit,
			)
		}

		return nil
	}
}

func (r *Repository) ShouldIgnoreFile(path string, ignorePatterns []string, includeGitignore bool) bool {
	// Check explicit ignore patterns
	for _, pattern := range ignorePatterns {
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}

	// Check .gitignore if enabled
	if includeGitignore {
		wt, err := r.repo.Worktree()
		if err != nil {
			logger.Error().Err(err).Msg(errors.ContextGitWorkTree)
			return false
		}

		patterns, err := gitignore.ReadPatterns(wt.Filesystem, nil)
		if err != nil {
			logger.Error().Err(err).Msg(errors.ContextGitIgnore)
			return false
		}

		matcher := gitignore.NewMatcher(patterns)
		if matcher.Match([]string{path}, false) {
			return true
		}
	}

	return false
}

func (r *Repository) Status() (gogit.Status, error) {
	w, err := r.repo.Worktree()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitWorkTree,
		)
	}

	status, err := w.Status()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitStatus,
		)
	}

	return status, nil
}

func (*Repository) generateDiff(old, current string) string {
	// Split content into lines and clean each line
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(current, "\n")

	var diff strings.Builder
	for i := 0; i < len(oldLines) || i < len(newLines); i++ {
		if i < len(oldLines) && i < len(newLines) && oldLines[i] == newLines[i] {
			continue
		}
		if i < len(oldLines) {
			// Clean and format removed lines
			line := cleanDiffLine(oldLines[i])
			diff.WriteString("- " + line + "\n")
		}
		if i < len(newLines) {
			// Clean and format added lines
			line := cleanDiffLine(newLines[i])
			diff.WriteString("+ " + line + "\n")
		}
	}

	return diff.String()
}

// cleanDiffLine standardizes a line for diff output
func cleanDiffLine(line string) string {
	// Replace tabs with spaces
	line = strings.ReplaceAll(line, "\t", "    ")

	// Trim any trailing whitespace
	line = strings.TrimRight(line, " \t")

	// Replace any remaining special characters if needed
	line = strings.ReplaceAll(line, "\r", "")

	return line
}

// GetFileStatus returns a string representation of a git status code
func GetFileStatus(status StatusCode) string {
	switch status {
	case Unmodified:
		return "M"
	case Added:
		return "A"
	case Modified:
		return "M"
	case Deleted:
		return "D"
	case Renamed:
		return "R"
	case Copied:
		return "C"
	case UpdatedButUnmerged:
		return "M"
	case Untracked:
		return "A"
	default:
		return "M"
	}
}
