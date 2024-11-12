package version

import (
	"context"
	"fmt"
	"os"
	"strings"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/git"
	"codeberg.org/mutker/bumpa/internal/llm"
	"codeberg.org/mutker/bumpa/internal/logger"
	"github.com/Masterminds/semver/v3"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	filePerms            = 0o600
	summaryCapacityRatio = 2 // Estimate initial capacity as half of total items
)

// Bumper manages version changes across files and git repository
type Bumper struct {
	cfg      *config.Config
	llm      llm.Client
	repo     *git.Repository
	current  *semver.Version
	proposed *semver.Version
	files    []config.VersionFile
	parser   *Parser
	strategy *Strategy
}

// Strategy defines keywords for version change detection
type Strategy struct {
	breakingKeywords []string
	featureKeywords  []string
}

// VersionStatus represents the current status of version objects
type VersionStatus struct {
	HasTag    bool
	HasCommit bool
}

type WorkflowState struct {
	Current     string
	Proposed    string
	Files       []string
	HasTag      bool
	HasCommit   bool
	NeedsTag    bool
	NeedsCommit bool
	SignTag     bool
	SignCommit  bool
}

// NewBumper creates a Bumper instance with configuration, LLM client, and git repository
func NewBumper(cfg *config.Config, llmClient llm.Client, repo *git.Repository) (*Bumper, error) {
	current, err := determineCurrentVersion(repo)
	if err != nil {
		return nil, err
	}

	strategy := &Strategy{
		breakingKeywords: []string{
			"!:",               // Conventional Commits breaking change indicator
			"BREAKING CHANGE:", // Explicit breaking change text
			"BREAKING-CHANGE:", // Alternative breaking change text
		},
		featureKeywords: []string{
			"feat:",
			"feature:",
			"add:",
			"implement:",
		},
	}

	return &Bumper{
		cfg:      cfg,
		llm:      llmClient,
		repo:     repo,
		current:  current,
		files:    cfg.Version.Files,
		parser:   New(current, strategy.breakingKeywords, strategy.featureKeywords),
		strategy: strategy,
	}, nil
}

func (b *Bumper) GetWorkflowState() (*WorkflowState, error) {
	if b.proposed == nil {
		return nil, errors.WrapWithContext(
			errors.CodeVersionError,
			errors.ErrInvalidInput,
			"no proposed version",
		)
	}

	status, err := b.CheckVersionObjects(b.proposed.String())
	if err != nil {
		return nil, err
	}

	return &WorkflowState{
		Current:     b.current.String(),
		Proposed:    b.proposed.String(),
		Files:       b.GetFilesToUpdate(),
		HasTag:      status.HasTag,
		HasCommit:   status.HasCommit,
		NeedsTag:    b.cfg.Version.Git.Tag && !status.HasTag,
		NeedsCommit: len(b.files) > 0 && b.cfg.Version.Git.Commit && !status.HasCommit,
		SignTag:     b.cfg.Version.Git.Signage,
		SignCommit:  b.cfg.Version.Git.Signage,
	}, nil
}

// GetCurrentVersion returns the current version string
func (b *Bumper) GetCurrentVersion() string {
	return b.current.String()
}

// GetFilesToUpdate returns paths of all files that will be updated during version change
func (b *Bumper) GetFilesToUpdate() []string {
	files := make([]string, 0, len(b.files))
	for _, f := range b.files {
		files = append(files, f.Path)
	}
	return files
}

// AnalyzeVersionChanges analyzes changes and suggests version bump
func (b *Bumper) AnalyzeVersionChanges(ctx context.Context) (string, error) {
	// If this is the initial version, propose 0.1.0 without any further analysis
	if b.current.String() == "0.1.0" {
		b.proposed = b.current
		return b.current.String(), nil
	}

	// Check if there are any changes to analyze
	status, err := b.repo.Status()
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitStatus,
		)
	}

	// If no changes, propose current version
	var hasChanges bool
	for path := range status {
		if !b.repo.ShouldIgnoreFile(path, b.cfg.Git.Ignore, b.cfg.Git.IncludeGitignore) {
			hasChanges = true
			break
		}
	}

	// If no changes, propose current version
	if !hasChanges {
		logger.Info().
			Str("version", b.current.String()).
			Msg("No changes detected. Using current version.")
		b.proposed = b.current
		return b.current.String(), nil
	}

	// Get file summaries and commit history
	fileSummaries, err := b.analyzeFiles(ctx)
	if err != nil {
		return "", err
	}

	commits, err := b.getChangesSinceLastVersion()
	if err != nil {
		return "", err
	}

	// Get suggestion from LLM
	suggestion, err := b.getVersionSuggestion(ctx, fileSummaries, commits)
	if err != nil {
		return "", err
	}

	// Parse and validate suggestion
	bumpType, preRelease, err := b.parser.ParseSuggestion(suggestion)
	if err != nil {
		return "", err
	}

	// Create proposed version
	proposed, err := ProposeVersion(b.current, bumpType, preRelease)
	if err != nil {
		return "", err
	}

	b.proposed = proposed
	return proposed.String(), nil
}

// GetProposedVersion returns the currently proposed version
func (b *Bumper) GetProposedVersion() *semver.Version {
	return b.proposed
}

// ClearProposedVersion clears the currently proposed version
func (b *Bumper) ClearProposedVersion() {
	b.proposed = nil
}

// ProposeVersionChange creates a new version based on bump type and prerelease
func (b *Bumper) ProposeVersionChange(bumpType, preRelease string) (string, error) {
	proposed, err := ProposeVersion(b.current, bumpType, preRelease)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeVersionError,
			err,
			fmt.Sprintf("%s: bump_type=%s, pre_release=%s",
				errors.ContextVersionPropose,
				bumpType,
				preRelease,
			),
		)
	}
	b.proposed = proposed

	logger.Debug().
		Str("current_version", b.current.String()).
		Str("proposed_version", proposed.String()).
		Str("bump_type", bumpType).
		Str("pre_release", preRelease).
		Msg("Version change proposed")

	return proposed.String(), nil
}

// ApplyVersionChange updates files and creates git objects according to configuration
func (b *Bumper) ApplyVersionChange(ctx context.Context) error {
	// Check if a proposed version exists
	if b.proposed == nil {
		return errors.WrapWithContext(
			errors.CodeVersionError,
			errors.ErrInvalidInput,
			errors.ContextVersionPropose,
		)
	}

	status, err := b.CheckVersionObjects(b.proposed.String())
	if err != nil {
		return err
	}

	// Determine what actions to take
	needsTag := b.cfg.Version.Git.Tag && !status.HasTag
	needsCommit := len(b.files) > 0 && b.cfg.Version.Git.Commit && !status.HasCommit

	if !needsTag && !needsCommit {
		logger.Debug().
			Str("current_version", b.current.String()).
			Str("proposed_version", b.proposed.String()).
			Msg("No version changes required")
		return nil
	}

	// Update files and create commit if needed
	if needsCommit {
		if err := b.updateFiles(); err != nil {
			return err
		}
		if err := b.commitVersionChange(ctx); err != nil {
			return err
		}
	}

	// Create tag if needed
	if needsTag {
		if err := b.createVersionTag(ctx); err != nil {
			return err
		}
	}

	logger.Info().
		Str("version", b.proposed.String()).
		Bool("files_updated", needsCommit).
		Bool("tag_created", needsTag).
		Msg("Version bump completed")

	return nil
}

// updateFiles modifies all configured files with the new version
func (b *Bumper) updateFiles() error {
	for _, file := range b.files {
		if err := b.updateFile(file); err != nil {
			return errors.WrapWithContext(
				errors.CodeInputError,
				err,
				"failed to update file: "+file.Path,
			)
		}
	}
	return nil
}

// updateFile updates a single file with the new version
// Creates a backup before modification and restores on failure
func (b *Bumper) updateFile(file config.VersionFile) error {
	logger.Info().
		Str("file", file.Path).
		Msg("Updating version in file")

	content, err := os.ReadFile(file.Path)
	if err != nil {
		return errors.WrapWithContext(
			errors.CodeInputError,
			err,
			errors.FormatContext(errors.ContextFileRead, file.Path),
		)
	}

	// Create backup
	backupPath := file.Path + ".bak"
	if err := os.WriteFile(backupPath, content, filePerms); err != nil {
		return errors.WrapWithContext(
			errors.CodeInputError,
			err,
			errors.FormatContext(errors.ContextFileWrite, backupPath),
		)
	}

	updated := string(content)
	for _, pattern := range file.Replace {
		old := strings.ReplaceAll(pattern, "{version}", b.current.String())
		replacement := strings.ReplaceAll(pattern, "{version}", b.proposed.String())
		updated = strings.ReplaceAll(updated, old, replacement)
	}

	if updated != string(content) {
		if err := os.WriteFile(file.Path, []byte(updated), filePerms); err != nil {
			// Attempt to restore backup on failure
			if renameErr := os.Rename(backupPath, file.Path); renameErr != nil {
				return errors.WrapWithContext(
					errors.CodeIOError,
					errors.ErrIO,
					errors.ContextFileRestore,
				)
			}
			return err
		}
	}

	// Remove backup
	os.Remove(backupPath)
	return nil
}

// commitVersionChange creates a version bump commit
func (b *Bumper) commitVersionChange(ctx context.Context) error {
	if !b.cfg.Version.Git.Commit {
		return nil
	}

	files := make([]string, 0, len(b.files))
	for _, f := range b.files {
		files = append(files, f.Path)
	}

	message := "chore(version): bump version to " + b.proposed.String()
	if err := b.repo.MakeCommit(ctx, message, files); err != nil {
		return errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to create version bump commit",
		)
	}

	logger.Info().
		Str("version", b.proposed.String()).
		Msg("Created version bump commit")

	return nil
}

// createVersionTag creates a git tag for the new version
func (b *Bumper) createVersionTag(ctx context.Context) error {
	if !b.cfg.Version.Git.Tag {
		return nil
	}

	tagName := "v" + b.proposed.String()
	tagMessage := "Version " + b.proposed.String()

	if err := b.repo.CreateTag(ctx, tagName, tagMessage); err != nil {
		return errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to create version tag",
		)
	}

	logger.Info().
		Str("tag", tagName).
		Msg("Created version tag")

	return nil
}

// analyzeFiles analyzes all changed files and returns summaries of significant changes
func (b *Bumper) analyzeFiles(ctx context.Context) ([]string, error) {
	status, err := b.repo.Status()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			errors.ContextGitStatus,
		)
	}

	// Pre-allocate with a reasonable initial capacity
	fileSummaries := make([]string, 0, len(status)/summaryCapacityRatio)
	for path, fileStatus := range status {
		if b.repo.ShouldIgnoreFile(path, b.cfg.Git.Ignore, b.cfg.Git.IncludeGitignore) {
			continue
		}

		summary, err := b.analyzeFile(ctx, path, fileStatus.Staging)
		if err != nil {
			return nil, err
		}
		fileSummaries = append(fileSummaries, summary)
	}

	if len(fileSummaries) == 0 {
		return nil, errors.WrapWithContext(
			errors.CodeNoChanges,
			errors.ErrInvalidInput,
			errors.ContextNoChanges,
		)
	}

	return fileSummaries, nil
}

// analyzeFile generates a summary of changes for a single file
func (b *Bumper) analyzeFile(ctx context.Context, path string, status git.StatusCode) (string, error) {
	diff, err := b.repo.GetFileDiff(path)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get diff for file: "+path,
		)
	}

	tool := config.FindFunction(b.cfg.Functions, "generate_file_summary")
	if tool == nil {
		return "", errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"generate_file_summary tool configuration not found",
		)
	}

	input := map[string]interface{}{
		"file":                  path,
		"status":                git.GetFileStatus(status),
		"diff":                  diff,
		"hasSignificantChanges": true, // Always consider changes significant for version analysis
	}

	summary, err := llm.CallFunction(ctx, b.llm, tool, input)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			err,
			"failed to analyze file: "+path,
		)
	}

	return path + ": " + summary, nil
}

// getVersionSuggestion requests version change suggestion from LLM
func (b *Bumper) getVersionSuggestion(ctx context.Context, fileSummaries []string, commits string) (string, error) {
	function := config.FindFunction(b.cfg.Functions, "analyze_version_bump")
	if function == nil {
		return "", errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			errors.ContextMissingFunctionConfig,
		)
	}

	input := map[string]interface{}{
		"current_version":   b.current.String(),
		"file_changes":      strings.Join(fileSummaries, "\n"),
		"commit_history":    commits,
		"breaking_keywords": b.strategy.breakingKeywords,
		"feature_keywords":  b.strategy.featureKeywords,
	}

	suggestion, err := llm.CallFunction(ctx, b.llm, function, input)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			err,
			errors.ContextLLMInvalidResponse,
		)
	}

	return strings.TrimSpace(suggestion), nil
}

// getChangesSinceLastVersion retrieves commit history since the last version tag
func (b *Bumper) getChangesSinceLastVersion() (string, error) {
	// Get the last version tag
	lastTag, err := b.findLastVersionTag()
	if err != nil {
		return "", err
	}

	// If no previous version tag exists, get all changes
	if lastTag == "" {
		messages, err := b.repo.GetAllCommitMessages()
		if err != nil {
			return "", err
		}
		return strings.Join(messages, "\n"), nil
	}

	// Get changes since the last tag
	messages, err := b.repo.GetChangesSinceTag(lastTag)
	if err != nil {
		return "", err
	}
	return strings.Join(messages, "\n"), nil
}

func (b *Bumper) CheckVersionObjects(version string) (VersionStatus, error) {
	result := VersionStatus{}

	// Check for tag
	refs, err := b.repo.References()
	if err != nil {
		return result, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get repository references",
		)
	}

	expectedMsg := "chore(version): bump version to " + version
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsTag() && ref.Name().Short() == "v"+version {
			result.HasTag = true
		}
		return nil
	})
	if err != nil {
		return result, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to iterate references",
		)
	}

	// Check recent commits for version bump message
	head, err := b.repo.Head()
	if err != nil {
		return result, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get HEAD",
		)
	}

	commit, err := b.repo.CommitObject(head.Hash())
	if err != nil {
		return result, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get commit",
		)
	}

	// Check last few commits (arbitrary limit to avoid checking entire history)
	for i := 0; i < 10 && commit != nil; i++ {
		if commit.Message == expectedMsg {
			result.HasCommit = true
			break
		}
		if len(commit.ParentHashes) == 0 {
			break
		}
		commit, err = b.repo.CommitObject(commit.ParentHashes[0])
		if err != nil {
			return result, errors.WrapWithContext(
				errors.CodeGitError,
				err,
				"failed to get parent commit",
			)
		}
	}

	return result, nil
}

// findLastVersionTag locates the most recent semantic version tag
func (b *Bumper) findLastVersionTag() (string, error) {
	return b.repo.FindLastVersionTag()
}

// determineCurrentVersion finds the current version from VERSION file or git tags
// Falls back to 0.1.0 if no version is found
func determineCurrentVersion(repo *git.Repository) (*semver.Version, error) {
	// First try VERSION file
	if content, err := os.ReadFile("VERSION"); err == nil {
		versionStr := strings.TrimSpace(string(content))
		if ver, err := semver.NewVersion(versionStr); err == nil {
			logger.Info().
				Str("source", "VERSION file").
				Str("version", ver.String()).
				Msg("Current version determined from VERSION file")
			return ver, nil
		}
	}

	// Try to get latest version tag
	refs, err := repo.References()
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to get repository references",
		)
	}

	var latestVer *semver.Version
	var latestTagName string
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsTag() {
			tagName := ref.Name().Short()
			versionStr := strings.TrimPrefix(tagName, "v")
			if ver, err := semver.NewVersion(versionStr); err == nil {
				if latestVer == nil || ver.GreaterThan(latestVer) {
					latestVer = ver
					latestTagName = tagName
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeGitError,
			err,
			"failed to iterate repository references",
		)
	}

	if latestVer != nil {
		logger.Info().
			Str("source", "git tag").
			Str("tag", latestTagName).
			Str("version", latestVer.String()).
			Msg("Current version determined from latest version tag")
		return latestVer, nil
	}

	// Start at 0.1.0
	initial, _ := semver.NewVersion("0.1.0")
	logger.Info().
		Str("source", "default").
		Str("version", initial.String()).
		Msg("No existing version found, starting at default version 0.1.0")
	return initial, nil
}
