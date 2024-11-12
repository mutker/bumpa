package version

import (
	"regexp"
	"strings"

	"codeberg.org/mutker/bumpa/internal/errors"
	"github.com/Masterminds/semver/v3"
)

const (
	splitPartsExpected = 2
	// Version bump types
	bumpTypeMajor = "major"
	bumpTypeMinor = "minor"
	bumpTypePatch = "patch"
	bumpTypeNone  = ""
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
func (p *Parser) ParseSuggestion(suggestion string) (string, string, error) {
	suggestion = strings.TrimSpace(suggestion)

	// Handle full version format (e.g., "0.1.0-beta1")
	if strings.Contains(suggestion, ".") {
		return p.parseFullVersion(suggestion)
	}

	// Handle simple format (e.g., "minor:beta1" or "beta2")
	return p.parseSimpleVersion(suggestion)
}

// parseFullVersion parses a complete version string (e.g., "1.2.3-beta1")
func (p *Parser) parseFullVersion(version string) (string, string, error) {
	ver, err := semver.NewVersion(version)
	if err != nil {
		return "", "", errors.WrapWithContext(
			errors.CodeValidateError,
			err,
			errors.ContextVersionInvalid,
		)
	}

	preRelease := ver.Prerelease()
	if preRelease != "" && !isValidPrerelease(preRelease) {
		return "", "", errors.WrapWithContext(
			errors.CodeValidateError,
			errors.ErrInvalidInput,
			errors.FormatContext(errors.ContextVersionPreRelease, preRelease),
		)
	}

	bumpType := p.determineBumpType(ver)
	return bumpType, preRelease, nil
}

// parseSimpleVersion parses a simplified version format (e.g., "minor:beta1" or "beta2")
func (*Parser) parseSimpleVersion(suggestion string) (string, string, error) {
	parts := strings.Split(suggestion, ":")

	switch len(parts) {
	case splitPartsExpected:
		bumpType := strings.TrimSpace(parts[0])
		preRelease := strings.TrimSpace(parts[1])
		if err := validateBumpType(bumpType); err != nil {
			return "", "", err
		}
		if preRelease != "" && !isValidPrerelease(preRelease) {
			return "", "", errors.WrapWithContext(
				errors.CodeValidateError,
				errors.ErrInvalidInput,
				errors.FormatContext(errors.ContextVersionPreRelease, preRelease),
			)
		}
		return bumpType, preRelease, nil
	case 1:
		suggestion = strings.TrimSpace(parts[0])
		if suggestion == "stable" {
			return bumpTypeNone, "", nil
		}
		if !isValidPrerelease(suggestion) {
			return "", "", errors.WrapWithContext(
				errors.CodeValidateError,
				errors.ErrInvalidInput,
				errors.FormatContext(errors.ContextVersionPreRelease, suggestion),
			)
		}
		return bumpTypeNone, suggestion, nil
	default:
		return "", "", errors.WrapWithContext(
			errors.CodeValidateError,
			errors.ErrInvalidInput,
			errors.ContextVersionInvalid,
		)
	}
}

// validateBumpType ensures the bump type is valid
func validateBumpType(bumpType string) error {
	switch bumpType {
	case bumpTypeMajor, bumpTypeMinor, bumpTypePatch, bumpTypeNone:
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
func isValidPrerelease(preRelease string) bool {
	return regexp.MustCompile(`^(alpha|beta|rc)\d+$`).MatchString(preRelease)
}

// determineBumpType compares a proposed version against the current version
func (p *Parser) determineBumpType(proposed *semver.Version) string {
	switch {
	case proposed.Major() > p.currentVersion.Major():
		return bumpTypeMajor
	case proposed.Minor() > p.currentVersion.Minor():
		return bumpTypeMinor
	case proposed.Patch() > p.currentVersion.Patch():
		return bumpTypePatch
	default:
		return bumpTypeNone
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
	case bumpTypeMajor:
		newVersion = current.IncMajor()
	case bumpTypeMinor:
		newVersion = current.IncMinor()
	case bumpTypePatch:
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
