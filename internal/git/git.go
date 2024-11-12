//nolint:wrapcheck // Using our own error wrapping system throughout package
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/logger"
	"github.com/Masterminds/semver"
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

func (r *Repository) References() (storer.ReferenceIter, error) {
	refs, err := r.repo.References()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get repository references",
		)
	}
	if err := refs.ForEach(func(*plumbing.Reference) error {
		return nil
	}); err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to validate references",
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
				return err // Return the error instead of nil
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

func (r *Repository) GetFileDiff(path string) (string, error) {
	logger.Debug().
		Str("path", path).
		Msg("Getting file diff")

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

	// Check if file was renamed
	for oldPath, fileStatus := range status {
		if fileStatus.Extra == path {
			logger.Debug().
				Str("old_path", oldPath).
				Str("new_path", path).
				Str("status", string(fileStatus.Staging)).
				Msg("Found renamed file")
			path = oldPath
			break
		}
	}

	fileStatus := status.File(path)
	logger.Debug().
		Str("path", path).
		Str("status", string(fileStatus.Staging)).
		Msg("File status")

	// Get old content from HEAD
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
			errors.ContextGitDiff,
		)
	}

	// Default to empty string for new files
	oldContent := ""
	file, err := commit.File(path)
	if err == nil {
		oldContent, err = file.Contents()
		if err != nil {
			return "", errors.WrapWithContext(
				errors.CodeGitError,
				err,
				errors.ContextGitDiff,
			)
		}
	} else if !errors.Is(err, object.ErrFileNotFound) {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitDiff,
		)
	}

	// Handle file statuses
	switch fileStatus.Staging {
	case Untracked:
		return newFileMessage, nil
	case Deleted:
		return deletedFileMessage, nil
	case Renamed:
		return fmt.Sprintf("[Renamed from %s]", fileStatus.Extra), nil
	case Modified, Added, Copied, UpdatedButUnmerged, Unmodified:
		// Handle all other cases with normal diff
		return r.generateDiff(oldContent, fileStatus, path)
	default:
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			errors.ErrInvalidInput,
			"unknown file status",
		)
	}
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
func (r *Repository) GetUserConfig() (string, string, error) {
	var name, email string
	var err error

	// With includeIf support, we should first try to get the effective config values
	// directly from git, letting it handle all the config resolution
	if isGitAvailable() {
		name, err = getConfigValue("user.name")
		if err != nil {
			return "", "", err
		}

		email, err = getConfigValue("user.email")
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

// FindLastVersionTag locates the most recent semantic version tag
func (r *Repository) FindLastVersionTag() (string, error) {
	refs, err := r.repo.References()
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get repository references",
		)
	}

	var lastTag string
	var latestVersion *semver.Version

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsTag() {
			// Extract version from tag name (remove 'v' prefix if present)
			tagName := ref.Name().Short()
			versionStr := strings.TrimPrefix(tagName, "v")

			// Try to parse as semantic version
			version, parseErr := semver.NewVersion(versionStr)
			if parseErr != nil {
				// Log the parsing error but continue iteration
				logger.Debug().
					Str("tag", tagName).
					Err(parseErr).
					Msg("Skipping invalid semantic version tag")
				return nil
			}

			// Update if this is the highest version seen
			if latestVersion == nil || version.GreaterThan(latestVersion) {
				latestVersion = version
				lastTag = tagName
			}
		}
		return nil
	})
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to iterate repository references",
		)
	}

	return lastTag, nil
}

// GetChangesSinceTag returns commit messages between the specified tag and HEAD
func (r *Repository) GetChangesSinceTag(tag string) ([]string, error) {
	refs, err := r.repo.References()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitDiff,
		)
	}

	var tagHash plumbing.Hash
	var found bool

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().Short() == tag {
			tagHash = ref.Hash()
			found = true
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitDiff,
		)
	}

	if !found {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			errors.ErrNotFound,
			errors.FormatContext(errors.ContextGitFileNotFound, tag),
		)
	}

	head, err := r.Head()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitBranch,
		)
	}

	return r.GetChangesBetween(tagHash, head.Hash())
}

// GetChangeHistory retrieves commit messages since a specific tag or all commits
func (r *Repository) GetChangeHistory(tag string) (string, error) {
	// If no tag is provided, get all commits
	if tag == "" {
		messages, err := r.GetAllCommitMessages()
		if err != nil {
			return "", err
		}
		return strings.Join(messages, "\n"), nil
	}

	// Get changes since the specified tag
	messages, err := r.GetChangesSinceTag(tag)
	if err != nil {
		return "", err
	}
	return strings.Join(messages, "\n"), nil
}

// GetChangesBetween returns commit messages between two commits
func (r *Repository) GetChangesBetween(from, to plumbing.Hash) ([]string, error) {
	var messages []string
	current, err := r.CommitObject(to)
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitCommit,
		)
	}

	for current != nil && current.Hash != from {
		messages = append(messages, strings.TrimSpace(current.Message))
		if len(current.ParentHashes) == 0 {
			break
		}

		current, err = r.CommitObject(current.ParentHashes[0])
		if err != nil {
			return nil, errors.WrapWithContext(
				errors.CodeGitError,
				err,
				errors.ContextGitCommit,
			)
		}
	}

	return messages, nil
}

// GetCommitMessagesSince returns all commit messages since a given hash
func (r *Repository) GetCommitMessagesSince(hash plumbing.Hash) ([]string, error) {
	var messages []string
	commit, err := r.CommitObject(hash)
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitCommit,
		)
	}

	for commit != nil {
		messages = append(messages, strings.TrimSpace(commit.Message))
		if len(commit.ParentHashes) == 0 {
			break
		}

		commit, err = r.CommitObject(commit.ParentHashes[0])
		if err != nil {
			return nil, errors.WrapWithContext(
				errors.CodeGitError,
				err,
				errors.ContextGitCommit,
			)
		}
	}

	return messages, nil
}

// GetAllCommitMessages returns all commit messages in the repository
func (r *Repository) GetAllCommitMessages() ([]string, error) {
	head, err := r.Head()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitBranch,
		)
	}

	return r.GetCommitMessagesSince(head.Hash())
}

// StageFiles stages the given files in the repository
func (r *Repository) StageFiles(files []string) error {
	w, err := r.repo.Worktree()
	if err != nil {
		return errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitWorkTree,
		)
	}

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

		// Stage files directly
		for _, file := range filesToAdd {
			_, err := w.Add(file)
			if err != nil {
				return errors.WrapWithContext(
					errors.CodeGitError,
					err,
					"failed to stage file: "+file,
				)
			}
		}

		// Get user configuration
		name, email, err := r.GetUserConfig()
		if err != nil {
			return err
		}

		// Create initial commit
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

		// Check if commit signing is enabled and available
		if isGitAvailable() {
			signStr, err := getConfigValue("commit.gpgsign")
			if err != nil {
				return errors.WrapWithContext(
					errors.CodeGitError,
					err,
					errors.ContextGitConfigReadError,
				)
			}

			if signStr == "true" {
				// Re-sign the commit using system git
				cmd := exec.Command("git", "commit", "--amend", "--no-edit", "--gpg-sign")
				cmd.Dir = w.Filesystem.Root()
				cmd.Env = append(os.Environ(), "GPG_TTY="+os.Getenv("TTY"))
				if err := cmd.Run(); err != nil {
					return errors.WrapWithContext(
						errors.CodeGitError,
						err,
						errors.ContextGitSigningFailed,
					)
				}
			}
		}

		return nil
	}
}

// CreateTag creates a new tag at HEAD with the given name and message
func (r *Repository) CreateTag(ctx context.Context, tagName, message string) error {
	select {
	case <-ctx.Done():
		return errors.Wrap(errors.CodeTimeoutError, ctx.Err())
	default:
		head, err := r.repo.Head()
		if err != nil {
			return errors.WrapWithContext(
				errors.CodeGitError,
				err,
				errors.ContextGitBranch,
			)
		}

		// Get user configuration for tag author
		name, email, err := r.GetUserConfig()
		if err != nil {
			return err
		}

		// Create tag using go-git
		_, err = r.repo.CreateTag(tagName, head.Hash(), &gogit.CreateTagOptions{
			Message: message,
			Tagger: &object.Signature{
				Name:  name,
				Email: email,
				When:  time.Now(),
			},
		})
		if err != nil {
			return errors.WrapWithContext(
				errors.CodeGitError,
				err,
				"failed to create tag",
			)
		}

		// Check if tag signing is enabled and available
		if isGitAvailable() {
			signStr, err := getConfigValue("tag.gpgsign")
			if err != nil {
				return errors.WrapWithContext(
					errors.CodeGitError,
					err,
					errors.ContextGitConfigReadError,
				)
			}

			if signStr == "true" {
				// Re-sign the tag using system git
				cmd := exec.Command("git", "tag", "-f", "-s", tagName, "-m", message)
				cmd.Env = append(os.Environ(), "GPG_TTY="+os.Getenv("TTY"))
				if err := cmd.Run(); err != nil {
					return errors.WrapWithContext(
						errors.CodeGitError,
						err,
						errors.ContextGitSigningFailed,
					)
				}
			}
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

func (r *Repository) generateDiff(oldContent string, input interface{}, path string) (string, error) {
	var diff string
	var maxLines int

	// Determine max lines and content based on input type
	switch v := input.(type) {
	case *gogit.FileStatus:
		// If input is a file status, use repository's configured max diff lines
		maxLines = r.cfg.MaxDiffLines

		// Handle special cases based on file status
		if v.Staging == Deleted {
			diff = r.generateLineDiff(oldContent, "")
		} else {
			// Read current content for modified files
			currentContent, err := os.ReadFile(path)
			if err != nil {
				return "", errors.WrapWithContext(
					errors.CodeGitError,
					err,
					errors.FormatContext(errors.ContextFileRead, path),
				)
			}
			diff = r.generateLineDiff(oldContent, string(currentContent))
		}
	case string:
		// If input is a string, generate diff between old content and input
		maxLines = 0 // No line limit for explicit string inputs
		strInput, ok := input.(string)
		if !ok {
			return "", errors.WrapWithContext(
				errors.CodeGitError,
				errors.ErrInvalidInput,
				"invalid input type for diff generation",
			)
		}
		diff = r.generateLineDiff(oldContent, strInput)
	default:
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			errors.ErrInvalidInput,
			"unsupported input type for diff generation",
		)
	}

	// Truncate diff if needed and max lines is set
	if maxLines > 0 && len(strings.Split(diff, "\n")) > maxLines {
		logger.Debug().
			Int("max_lines", maxLines).
			Msg("Truncating diff")
		diff = strings.Join(strings.Split(diff, "\n")[:maxLines], "\n") + "\n..."
	}

	return diff, nil
}

// generateLineDiff performs the core line-by-line diff generation
func (*Repository) generateLineDiff(old, current string) string {
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
