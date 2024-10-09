package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/rs/zerolog/log"
)

type Repository struct {
	repo *git.Repository
}

var ErrNoChanges = errors.New("no changes to commit")

func OpenRepository(path string) (*Repository, error) {
	repo, err := git.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open git repository: %w", err)
	}
	return &Repository{repo: repo}, nil
}

func (r *Repository) GetRepo() *git.Repository {
	return r.repo
}

func (r *Repository) GetCurrentBranch() (string, error) {
	head, err := r.repo.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD: %w", err)
	}

	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}

	refs, err := r.repo.References()
	if err != nil {
		return "", fmt.Errorf("failed to get references: %w", err)
	}

	var closestBranch string
	var closestCommit *object.Commit
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() {
			commit, err := r.repo.CommitObject(ref.Hash())
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

func (r *Repository) GetFileDiff(path string) (string, error) {
	w, err := r.repo.Worktree()
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

	head, err := r.repo.Head()
	if err != nil {
		return "", err
	}

	commit, err := r.repo.CommitObject(head.Hash())
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

	diff := generateSimpleDiff(oldContent, string(currentContent))
	return summarizeDiff(diff), nil
}

func generateSimpleDiff(old, new string) string {
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

func summarizeDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	if len(lines) > 10 {
		return strings.Join(lines[:10], "\n") + "\n..."
	}
	return diff
}

func (r *Repository) GetFilesToCommit() ([]string, error) {
	w, err := r.repo.Worktree()
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

	if len(files) == 0 {
		return nil, ErrNoChanges
	}

	sort.Strings(files)
	return files, nil
}

func (r *Repository) MakeCommit(message string, filesToAdd []string) error {
	w, err := r.repo.Worktree()
	if err != nil {
		return err
	}

	for _, file := range filesToAdd {
		_, err := w.Add(file)
		if err != nil {
			return fmt.Errorf("failed to add file %s: %w", file, err)
		}
	}

	cfg, err := r.repo.Config()
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

func (r *Repository) ShouldIgnoreFile(path string, ignorePatterns []string, includeGitignore bool) bool {
	// Check against the ignore list in the config
	for _, pattern := range ignorePatterns {
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}

	// Check against .gitignore if includeGitignore is true
	if includeGitignore {
		wt, err := r.repo.Worktree()
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

func GetFileStatus(status git.StatusCode) string {
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
