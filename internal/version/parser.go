package version

import (
	"regexp"
	"strings"

	"codeberg.org/mutker/bumpa/internal/errors"
	"github.com/Masterminds/semver/v3"
)

// Parser handles semantic version parsing and validation
type Parser struct {
	currentVersion   *semver.Version
	breakingKeywords []string
	featureKeywords  []string
}

// New creates a Parser instance with the current version and keyword lists for change analysis
func New(current *semver.Version, breakingKeywords, featureKeywords []string) *Parser {
	return &Parser{
		currentVersion:   current,
		breakingKeywords: breakingKeywords,
		featureKeywords:  featureKeywords,
	}
}

// ParseSuggestion parses a version suggestion string into bump type and prerelease components.
// Accepts both full versions (e.g., "1.2.3-beta1") and simple formats (e.g., "minor:beta1" or "beta2")
func (p *Parser) ParseSuggestion(suggestion string) (bumpType, preRelease string, err error) {
	suggestion = strings.TrimSpace(suggestion)

	// Handle full version format (e.g., "0.1.0-beta1")
	if strings.Contains(suggestion, ".") {
		return p.parseFullVersion(suggestion)
	}

	// Handle simple format (e.g., "minor:beta1" or "beta2")
	return p.parseSimpleFormat(suggestion)
}

// parseFullVersion parses a complete version string (e.g., "1.2.3-beta1")
func (p *Parser) parseFullVersion(version string) (bumpType, preRelease string, err error) {
	ver, err := semver.NewVersion(version)
	if err != nil {
		return "", "", errors.WrapWithContext(
			errors.CodeValidateError,
			err,
			errors.ContextVersionInvalid,
		)
	}

	preRelease = ver.Prerelease()
	if preRelease != "" && !p.isValidPrerelease(preRelease) {
		return "", "", errors.WrapWithContext(
			errors.CodeValidateError,
			errors.ErrInvalidInput,
			errors.FormatContext(errors.ContextVersionPreRelease, preRelease),
		)
	}

	bumpType = p.determineBumpType(ver)
	return bumpType, preRelease, nil
}

// parseSimpleFormat parses a simplified version format (e.g., "minor:beta1" or "beta2")
func (p *Parser) parseSimpleFormat(suggestion string) (bumpType, preRelease string, err error) {
	parts := strings.Split(suggestion, ":")

	switch len(parts) {
	case 2:
		bumpType = strings.TrimSpace(parts[0])
		preRelease = strings.TrimSpace(parts[1])
	case 1:
		suggestion = strings.TrimSpace(parts[0])
		if suggestion == "stable" {
			preRelease = ""
		} else {
			preRelease = suggestion
		}
	default:
		return "", "", errors.WrapWithContext(
			errors.CodeValidateError,
			errors.ErrInvalidInput,
			errors.ContextVersionInvalid,
		)
	}

	if err := p.validateBumpType(bumpType); err != nil {
		return "", "", err
	}

	if preRelease != "" && !p.isValidPrerelease(preRelease) {
		return "", "", errors.WrapWithContext(
			errors.CodeValidateError,
			errors.ErrInvalidInput,
			errors.FormatContext(errors.ContextVersionPreRelease, preRelease),
		)
	}

	return bumpType, preRelease, nil
}

// validateBumpType ensures the bump type is one of: major, minor, patch, or empty
func (p *Parser) validateBumpType(bumpType string) error {
	switch bumpType {
	case "major", "minor", "patch", "":
		return nil
	default:
		return errors.WrapWithContext(
			errors.CodeValidateError,
			errors.ErrInvalidInput,
			errors.FormatContext(errors.ContextVersionBumpType, bumpType),
		)
	}
}

// isValidPrerelease checks if the prerelease suffix matches the pattern: alpha|beta|rc + number
func (p *Parser) isValidPrerelease(preRelease string) bool {
	return regexp.MustCompile(`^(alpha|beta|rc)\d+$`).MatchString(preRelease)
}

// determineBumpType compares a proposed version against the current version
func (p *Parser) determineBumpType(proposed *semver.Version) string {
	switch {
	case proposed.Major() > p.currentVersion.Major():
		return "major"
	case proposed.Minor() > p.currentVersion.Minor():
		return "minor"
	case proposed.Patch() > p.currentVersion.Patch():
		return "patch"
	default:
		return ""
	}
}

// ProposeVersion creates a new version based on the current version and requested changes
func ProposeVersion(current *semver.Version, bumpType, preRelease string) (*semver.Version, error) {
	if current == nil {
		return nil, errors.WrapWithContext(
			errors.CodeValidateError,
			errors.ErrInvalidInput,
			"current version is required",
		)
	}

	var newVersion semver.Version
	switch bumpType {
	case "major":
		newVersion = current.IncMajor()
	case "minor":
		newVersion = current.IncMinor()
	case "patch":
		newVersion = current.IncPatch()
	default:
		newVersion = *current
	}

	if preRelease != "" {
		ver, err := newVersion.SetPrerelease(preRelease)
		if err != nil {
			return nil, errors.WrapWithContext(
				errors.CodeValidateError,
				err,
				"failed to set pre-release suffix",
			)
		}
		return &ver, nil
	}

	return &newVersion, nil
}
