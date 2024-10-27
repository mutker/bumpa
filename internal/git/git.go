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
			"failed to open repository",
		)
	}

	return &Repository{repo: repo, cfg: cfg}, nil
}

func (r *Repository) Head() (*plumbing.Reference, error) {
	head, err := r.repo.Head()
	if err != nil {
		return nil, errors.Wrap(errors.CodeGitError, err)
	}

	return head, nil
}

//nolint:ireturn // Interface return needed for go-git compatibility
func (r *Repository) References() (storer.ReferenceIter, error) {
	refs, err := r.repo.References()
	if err != nil {
		return nil, errors.Wrap(errors.CodeGitError, err)
	}

	return refs, nil
}

func (r *Repository) CommitObject(hash plumbing.Hash) (*object.Commit, error) {
	commit, err := r.repo.CommitObject(hash)
	if err != nil {
		return nil, errors.Wrap(errors.CodeGitError, err)
	}

	return commit, nil
}

func (r *Repository) GetCurrentBranch() (string, error) {
	head, err := r.Head()
	if err != nil {
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}

	refs, err := r.repo.References()
	if err != nil {
		return "", errors.Wrap(errors.CodeGitError, err)
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
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	if closestBranch == "" {
		return "DETACHED_HEAD", nil
	}

	return closestBranch, nil
}

//nolint:cyclop // Complex function handling multiple git operations
func (r *Repository) GetFileDiff(path string) (string, error) {
	w, err := r.repo.Worktree()
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get worktree",
		)
	}

	status, err := w.Status()
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get status",
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
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	// Get old content
	head, err := r.Head()
	if err != nil {
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	commit, err := r.CommitObject(head.Hash())
	if err != nil {
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	file, err := commit.File(path)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return newFileMessage, nil
		}

		return "", errors.Wrap(errors.CodeGitError, err)
	}

	oldContent, err := file.Contents()
	if err != nil {
		return "", errors.Wrap(errors.CodeGitError, err)
	}

	// Generate and truncate diff if needed
	diff := r.generateDiff(oldContent, string(currentContent))
	if len(strings.Split(diff, "\n")) > r.cfg.MaxDiffLines {
		diff = strings.Join(strings.Split(diff, "\n")[:r.cfg.MaxDiffLines], "\n") + "\n..."
	}

	return diff, nil
}

func (*Repository) generateDiff(old, current string) string {
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(current, "\n")

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

func (r *Repository) GetFilesToCommit() ([]string, error) {
	logger.Debug().Msg("Getting files to commit")

	w, err := r.repo.Worktree()
	if err != nil {
		return nil, errors.Wrap(errors.CodeGitError, err)
	}

	status, err := w.Status()
	if err != nil {
		return nil, errors.Wrap(errors.CodeGitError, err)
	}

	var files []string
	for file, fileStatus := range status {
		if fileStatus.Staging != Unmodified || fileStatus.Worktree != Unmodified {
			files = append(files, file)
		}
	}

	if len(files) == 0 {
		logger.Info().Msg("No changes to commit")
		return nil, errors.New(errors.CodeInvalidState)
	}

	logger.Debug().Int("fileCount", len(files)).Msg("Files to commit")

	return files, nil
}

func (r *Repository) MakeCommit(ctx context.Context, message string, filesToAdd []string) error {
	select {
	case <-ctx.Done():
		return errors.Wrap(errors.CodeTimeoutError, ctx.Err())
	default:
		w, err := r.repo.Worktree()
		if err != nil {
			return errors.Wrap(errors.CodeGitError, err)
		}

		for _, file := range filesToAdd {
			_, err := w.Add(file)
			if err != nil {
				return errors.Wrap(errors.CodeGitError, err)
			}
		}

		cfg, err := r.repo.Config()
		if err != nil {
			return errors.Wrap(errors.CodeGitError, err)
		}

		name := cfg.User.Name
		email := cfg.User.Email

		if name == "" || email == "" {
			return errors.New(errors.CodeInputError)
		}

		_, err = w.Commit(message, &gogit.CommitOptions{
			Author: &object.Signature{
				Name:  name,
				Email: email,
				When:  time.Now(),
			},
			All: true,
		})
		if err != nil {
			logger.Error().Err(err).Msg("Failed to create commit")
			return errors.Wrap(errors.CodeGitError, err)
		}

		logger.Info().Msg("Commit created successfully")

		return nil
	}
}

func (r *Repository) ShouldIgnoreFile(path string, ignorePatterns []string, includeGitignore bool) bool {
	for _, pattern := range ignorePatterns {
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}

	if includeGitignore {
		wt, err := r.repo.Worktree()
		if err != nil {
			logger.Error().Err(err).Msg("Failed to get worktree")
			return false
		}

		patterns, err := gitignore.ReadPatterns(wt.Filesystem, nil)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to read gitignore patterns")
			return false
		}

		matcher := gitignore.NewMatcher(patterns)
		if matcher.Match([]string{path}, false) {
			return true
		}
	}

	return false
}

//nolint:wrapcheck // Direct passthrough of go-git status
func (r *Repository) Status() (gogit.Status, error) {
	w, err := r.repo.Worktree()
	if err != nil {
		return nil, errors.Wrap(errors.CodeGitError, err)
	}

	return w.Status()
}

// GetFileStatus returns a string representation of a git status code
func GetFileStatus(status StatusCode) string {
	switch status {
	case Unmodified:
		return "Unmodified"
	case Added:
		return "Added"
	case Modified:
		return "Modified"
	case Deleted:
		return "Deleted"
	case Renamed:
		return "Renamed"
	case Copied:
		return "Copied"
	case UpdatedButUnmerged:
		return "UpdatedButUnmerged"
	case Untracked:
		return "Untracked"
	default:
		return "Unknown"
	}
}
