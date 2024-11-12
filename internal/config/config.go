package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/logger"
	"github.com/spf13/viper"
)

const (
	DefaultMaxRetries       = 3
	DefaultMaxDiffLines     = 10
	DefaultCommitMsgTimeout = 30 * time.Second
	DefaultRequestTimeout   = 30 * time.Second
	DefaultLogFilePerms     = os.FileMode(0o666)
	DefaultLogDirPerms      = os.FileMode(0o755)
	DefaultPermissionsMask  = os.FileMode(0o777)
	DefaultLineLength       = 72

	// Common time formats
	TimeFormatRFC3339 = "2006-01-02T15:04:05Z07:00"
	TimeFormatUnix    = "2006-01-02 15:04:05"
	TimeFormatSimple  = "2006-01-02"
)

type Config struct {
	Logging   LoggingConfig `mapstructure:"logging"`
	Git       GitConfig     `mapstructure:"git"`
	LLM       LLMConfig     `mapstructure:"llm"`
	Functions []LLMFunction `mapstructure:"functions"`
	Command   string        `mapstructure:"command"`
	Version   VersionConfig `mapstructure:"version"`
	NoConfirm bool          `mapstructure:"no_confirm"`
}

type GitConfig struct {
	IncludeGitignore    bool     `mapstructure:"include_gitignore"`
	Ignore              []string `mapstructure:"ignore"`
	MaxDiffLines        int      `mapstructure:"max_diff_lines"`
	PreferredLineLength int      `mapstructure:"preferred_line_length"`
}

type CLIConfig struct {
	Command string
}

type LoggingConfig struct {
	Environment  string              `mapstructure:"environment"`
	TimeFormat   string              `mapstructure:"timeformat"`
	Output       string              `mapstructure:"output"`
	Level        string              `mapstructure:"level"`
	Path         string              `mapstructure:"file_path"`
	FilePerms    os.FileMode         `mapstructure:"file_perms"`
	DirPerms     os.FileMode         `mapstructure:"dir_perms"`
	Environments []EnvironmentConfig `mapstructure:"environments"`
}

type EnvironmentConfig struct {
	Name       string      `mapstructure:"name"`
	TimeFormat string      `mapstructure:"timeformat"`
	Output     string      `mapstructure:"output"`
	Level      string      `mapstructure:"level"`
	Path       string      `mapstructure:"file_path,omitempty"`
	FilePerms  os.FileMode `mapstructure:"file_perms,omitempty"`
	DirPerms   os.FileMode `mapstructure:"dir_perms,omitempty"`
}

type LLMConfig struct {
	Provider         string
	Model            string
	BaseURL          string        `mapstructure:"base_url"`
	APIKey           string        `mapstructure:"api_key,omitempty"` // Make API key optional
	MaxRetries       int           `mapstructure:"max_retries"`
	CommitMsgTimeout time.Duration `mapstructure:"commit_msg_timeout"`
	RequestTimeout   time.Duration `mapstructure:"request_timeout"`
}

type LLMFunction struct {
	Name         string             `mapstructure:"name"          yaml:"name"`
	Description  string             `mapstructure:"description"   yaml:"description"`
	Parameters   FunctionParameters `mapstructure:"parameters"    yaml:"parameters"`
	SystemPrompt string             `mapstructure:"system_prompt" yaml:"system_prompt"` //nolint:tagliatelle // Following OpenAI API spec
	UserPrompt   string             `mapstructure:"user_prompt"   yaml:"user_prompt"`   //nolint:tagliatelle // Following OpenAI API spec
}

type FunctionParameters struct {
	Type       string              `mapstructure:"type"       yaml:"type"`
	Properties map[string]Property `mapstructure:"properties" yaml:"properties"`
	Required   []string            `mapstructure:"required"   yaml:"required"`
}

type Property struct {
	Type        string    `mapstructure:"type"           yaml:"type"`
	Description string    `mapstructure:"description"    yaml:"description"`
	Enum        []string  `mapstructure:"enum,omitempty" yaml:"enum,omitempty"`
	Items       *Property `mapstructure:"items,omitempty" yaml:"items,omitempty"`
}

type VersionConfig struct {
	Current    string        `mapstructure:"current"`
	Git        VersionGit    `mapstructure:"git"`
	Prerelease []string      `mapstructure:"prerelease"`
	Files      []VersionFile `mapstructure:"files"`
	Alpha      bool          `mapstructure:"alpha"`
	Beta       bool          `mapstructure:"beta"`
	RC         bool          `mapstructure:"rc"`
}

type VersionGit struct {
	Commit  bool `yaml:"commit"`
	Tag     bool `yaml:"tag"`
	Signage bool `yaml:"signage"`
}

type VersionFile struct {
	Path    string   `yaml:"path"`
	Replace []string `yaml:"replace"`
}

func Load() (*Config, error) {
	viper.Reset()

	// Enable environment variables first
	viper.AutomaticEnv()
	viper.SetEnvPrefix("BUMPA")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Set config file settings
	viper.SetConfigName(".bumpa")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	SetDefaults()

	if err := viper.ReadInConfig(); err != nil {
		var configFileNotFound viper.ConfigFileNotFoundError
		if errors.As(err, &configFileNotFound) {
			logger.Warn().Msg(errors.ContextConfigNotFound)
		} else {
			return nil, errors.WrapWithContext(
				errors.CodeConfigError,
				err,
				errors.ContextConfigNotFound,
			)
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		logger.Error().
			Err(err).
			Msg("Failed to unmarshal configuration")
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			err,
			errors.ContextConfigUnmarshal,
		)
	}

	// Validate configuration
	if err := validateConfig(&cfg); err != nil {
		logger.Error().
			Err(err).
			Msg("Configuration validation failed")
		return nil, err
	}

	// Parse command line flags last to override file config
	if err := ParseFlags(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func LoadInitialLogging() (*LoggingConfig, error) {
	viper.Reset()

	// Enable environment variables
	viper.AutomaticEnv()
	viper.SetEnvPrefix("BUMPA")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Bind logging-related environment variables
	envVars := []string{
		"logging.level",
		"logging.environment",
		"logging.output",
		"logging.timeformat",
	}

	for _, env := range envVars {
		if err := viper.BindEnv(env); err != nil {
			return nil, errors.WrapWithContext(
				errors.CodeConfigError,
				err,
				"failed to bind environment variable: %s"+env,
			)
		}
	}

	SetDefaults()

	// Validate log level
	logLevel := viper.GetString("logging.level")
	if !isValidLogLevel(logLevel) {
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			fmt.Sprintf("invalid log level '%s'. Valid levels are: debug, info, warn, error, fatal", logLevel),
		)
	}

	if err := viper.ReadInConfig(); err != nil {
		var configFileNotFound viper.ConfigFileNotFoundError
		if errors.As(err, &configFileNotFound) {
			return &LoggingConfig{
				Environment: viper.GetString("logging.environment"),
				TimeFormat:  viper.GetString("logging.timeformat"),
				Output:      viper.GetString("logging.output"),
				Level:       viper.GetString("logging.level"),
				FilePerms:   DefaultLogFilePerms,
				DirPerms:    DefaultLogDirPerms,
			}, nil
		}
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			err,
			errors.ContextConfigNotFound,
		)
	}

	var cfg struct {
		Logging LoggingConfig `mapstructure:"logging"`
	}

	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			err,
			errors.ContextConfigUnmarshal,
		)
	}

	// Override with environment variables if set
	if viper.IsSet("logging.level") {
		cfg.Logging.Level = viper.GetString("logging.level")
	}
	if viper.IsSet("logging.environment") {
		cfg.Logging.Environment = viper.GetString("logging.environment")
	}

	return &cfg.Logging, nil
}

func validateConfig(cfg *Config) error {
	if cfg == nil {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"configuration is required",
		)
	}

	// Validate function prompts
	for i := range cfg.Functions {
		if strings.TrimSpace(cfg.Functions[i].SystemPrompt) == "" {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				errors.FormatContext(errors.ContextMissingPrompt, "system", cfg.Functions[i].Name),
			)
		}
		if strings.TrimSpace(cfg.Functions[i].UserPrompt) == "" {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				errors.FormatContext(errors.ContextMissingPrompt, "user", cfg.Functions[i].Name),
			)
		}
	}

	// Validate required functions exist
	if !hasRequiredFunctions(cfg.Functions) {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			errors.ContextMissingFunctionConfig,
		)
	}

	return nil
}

func (c *EnvironmentConfig) Validate() error {
	// Validate time format
	if c.TimeFormat != "" {
		referenceTime := time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC)
		var formatErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					formatErr = errors.WrapWithContext(
						errors.CodeConfigError,
						errors.ErrInvalidInput,
						errors.ContextInvalidTimeFormat,
					)
				}
			}()
			_ = referenceTime.Format(c.TimeFormat)
		}()
		if formatErr != nil {
			return formatErr
		}
	}

	// Validate output and file path
	if c.Output == "file" {
		if c.Path == "" {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				"file output requires file_path to be set",
			)
		}

		dir := filepath.Dir(c.Path)
		if err := os.MkdirAll(dir, DefaultLogDirPerms); err != nil {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				err,
				errors.FormatContext(errors.ContextDirCreate, dir),
			)
		}

		perms := safeFileMode(int(c.FilePerms))
		f, err := os.OpenFile(c.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, perms)
		if err != nil {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				err,
				errors.FormatContext(errors.ContextFileCreate, c.Path),
			)
		}
		f.Close()
	}

	// Validate log level
	if !isValidLogLevel(c.Level) {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			errors.ContextInvalidLogLevel,
		)
	}

	return nil
}

func (c *LoggingConfig) ToLoggerConfig() logger.Config {
	return logger.Config{
		Environment: c.Environment,
		TimeFormat:  c.TimeFormat,
		Output:      c.Output,
		Level:       c.Level,
		Path:        c.Path,
		FilePerms:   c.FilePerms,
	}
}

func (c *LoggingConfig) ActiveEnvironment() *EnvironmentConfig {
	envName := getEnvOrDefault("BUMPA_ENVIRONMENT", c.Environment)

	if c.Environments != nil {
		for i := range c.Environments {
			if c.Environments[i].Name == envName {
				return &c.Environments[i]
			}
		}
	}

	// Fall back to legacy single-environment config
	return &EnvironmentConfig{
		Name:       envName,
		TimeFormat: c.TimeFormat,
		Output:     c.Output,
		Level:      c.Level,
		Path:       c.Path,
		FilePerms:  c.FilePerms,
		DirPerms:   c.DirPerms,
	}
}

func (v *VersionConfig) Validate() error {
	// Check that only one pre-release type is set
	preReleaseCount := 0
	if v.Alpha {
		preReleaseCount++
	}
	if v.Beta {
		preReleaseCount++
	}
	if v.RC {
		preReleaseCount++
	}

	if preReleaseCount > 1 {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"only one pre-release type (alpha, beta, rc) can be set",
		)
	}

	// Validate prerelease identifiers if specified
	for _, pre := range v.Prerelease {
		if !isValidPrerelease(pre) {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				"invalid prerelease identifier: "+pre,
			)
		}
	}

	return nil
}

func ParseFlags(cfg *Config) error {
	flagSet := flag.NewFlagSet("bumpa", flag.ExitOnError)

	// Version flags
	alpha := flagSet.Bool("alpha", false, "Mark as alpha release")
	beta := flagSet.Bool("beta", false, "Mark as beta release")
	rc := flagSet.Bool("rc", false, "Mark as release candidate")
	noConfirm := flagSet.Bool("no-confirm", false, "Skip confirmation prompts")

	if err := flagSet.Parse(os.Args[1:]); err != nil {
		return errors.Wrap(errors.CodeInputError, err)
	}

	// Handle version flags
	if *alpha && *beta || *alpha && *rc || *beta && *rc {
		return errors.WrapWithContext(
			errors.CodeInputError,
			errors.ErrInvalidInput,
			"only one of -alpha, -beta, or -rc can be specified",
		)
	}

	// Get command from first non-flag argument
	if flagSet.NArg() > 0 {
		cfg.Command = flagSet.Arg(0)
	}

	if cfg.Command == "" {
		return errors.WrapWithContext(
			errors.CodeInputError,
			errors.ErrInvalidInput,
			errors.ContextNoCommand,
		)
	}

	cfg.Version.Alpha = *alpha
	cfg.Version.Beta = *beta
	cfg.Version.RC = *rc
	cfg.NoConfirm = *noConfirm

	return nil
}

func SetDefaults() {
	viper.SetDefault("llm.provider", "openai-compatible")
	viper.SetDefault("llm.model", "llama3.1:latest")
	viper.SetDefault("llm.base_url", "http://localhost:11434/v1")
	viper.SetDefault("llm.api_key", "")
	viper.SetDefault("llm.max_retries", DefaultMaxRetries)
	viper.SetDefault("llm.commit_msg_timeout", DefaultCommitMsgTimeout)
	viper.SetDefault("llm.request_timeout", DefaultRequestTimeout)
	viper.SetDefault("logging.environment", "development")
	viper.SetDefault("logging.timeformat", TimeFormatRFC3339)
	viper.SetDefault("logging.output", "console")
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.file_perms", int(DefaultLogFilePerms))
	viper.SetDefault("logging.dir_perms", int(DefaultLogDirPerms))
	viper.SetDefault("git.include_gitignore", true)
	viper.SetDefault("git.max_diff_lines", DefaultMaxDiffLines)
	viper.SetDefault("git.preferred_line_length", DefaultLineLength)

	// Add defaults for version config
	viper.SetDefault("version.current", "0.1.0")
	viper.SetDefault("version.alpha", false)
	viper.SetDefault("version.beta", false)
	viper.SetDefault("version.rc", false)
	viper.SetDefault("no_confirm", false)

	// Add environment variable mappings
	envMappings := map[string]string{
		"logging.level":       "LOG_LEVEL",
		"logging.environment": "ENVIRONMENT",
		"logging.output":      "LOG_OUTPUT",
		"logging.timeformat":  "LOG_TIMEFORMAT",
		"logging.file_path":   "LOG_FILE",
		"llm.api_key":         "LLM_API_KEY",
		"llm.base_url":        "LLM_BASE_URL",
		"llm.model":           "LLM_MODEL",
	}

	for configKey, envKey := range envMappings {
		if err := viper.BindEnv(configKey, "BUMPA_"+envKey); err != nil {
			logger.Warn().
				Str("config_key", configKey).
				Str("env_key", envKey).
				Err(err).
				Msg("Failed to bind environment variable")
		}
	}
}

// Helper function for safe permission conversion
//
//nolint:gosec // Safe conversion as we explicitly mask to valid permission bits
func safeFileMode(perms int) os.FileMode {
	return os.FileMode(perms & int(DefaultLogFilePerms))
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func isValidLogLevel(level string) bool {
	switch strings.ToLower(level) {
	case "debug", "info", "warn", "error", "fatal":
		return true
	default:
		return false
	}
}

// isValidPrerelease checks if a prerelease identifier is valid according to semver
func isValidPrerelease(pre string) bool {
	// Simple validation for now: alphanumeric and hyphen only
	return regexp.MustCompile(`^[0-9A-Za-z-]+$`).MatchString(pre)
}

func FindFunction(functions []LLMFunction, name string) *LLMFunction {
	for i := range functions {
		if functions[i].Name == name {
			return &functions[i]
		}
	}
	return nil
}

func hasRequiredFunctions(functions []LLMFunction) bool {
	required := map[string]bool{
		"generate_file_summary":   false,
		"generate_commit_message": false,
		"analyze_version_bump":    false,
		"retry_commit_message":    false,
	}

	for i := range functions {
		if _, ok := required[functions[i].Name]; ok {
			required[functions[i].Name] = true
		}
	}

	for _, found := range required {
		if !found {
			return false
		}
	}

	return true
}
