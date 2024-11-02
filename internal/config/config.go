package config

import (
	"flag"
	"os"
	"path/filepath"
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
	Logging LoggingConfig
	Git     GitConfig
	LLM     LLMConfig
	Tools   []Tool `mapstructure:"tools"`
	Command string
}

type CLIConfig struct {
	Command string
}

// LoggingConfig represents logging configuration from config file
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

type GitConfig struct {
	IncludeGitignore    bool     `mapstructure:"include_gitignore"`
	Ignore              []string `mapstructure:"ignore"`
	MaxDiffLines        int      `mapstructure:"max_diff_lines"`
	PreferredLineLength int      `mapstructure:"preferredLineLength"`
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

type Tool struct {
	Name         string   `mapstructure:"name"          yaml:"name"`
	Type         string   `mapstructure:"type"          yaml:"type"`
	Model        string   `mapstructure:"model"         yaml:"model"`
	Function     Function `mapstructure:"function"      yaml:"function"`
	SystemPrompt string   `mapstructure:"system_prompt" yaml:"system_prompt"` //nolint:tagliatelle // Maintaining consistency
	UserPrompt   string   `mapstructure:"user_prompt"   yaml:"user_prompt"`   //nolint:tagliatelle // Maintaining consistency
}

type Function struct {
	Name        string     `mapstructure:"name"        yaml:"name"`
	Description string     `mapstructure:"description" yaml:"description"`
	Parameters  Parameters `mapstructure:"parameters"  yaml:"parameters"`
}

type Parameters struct {
	Type       string              `mapstructure:"type"       yaml:"type"`
	Properties map[string]Property `mapstructure:"properties" yaml:"properties"`
	Required   []string            `mapstructure:"required"   yaml:"required"`
}

type Property struct {
	Type        string   `mapstructure:"type"           yaml:"type"`
	Description string   `mapstructure:"description"    yaml:"description"`
	Enum        []string `mapstructure:"enum,omitempty" yaml:"enum,omitempty"`
}

func Load() (*Config, error) {
	viper.Reset()

	viper.SetConfigName(".bumpa")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	// Bind environment variables
	if err := viper.BindEnv("logging.level", "BUMPA_LOG_LEVEL"); err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			err,
			"failed to bind BUMPA_LOG_LEVEL environment variable",
		)
	}
	if err := viper.BindEnv("logging.environment", "BUMPA_ENVIRONMENT"); err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			err,
			"failed to bind BUMPA_ENVIRONMENT environment variable",
		)
	}

	// Enable environment variables
	viper.AutomaticEnv()
	viper.SetEnvPrefix("BUMPA")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	setDefaults()

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
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			err,
			errors.ContextConfigUnmarshal,
		)
	}

	// Validate configuration
	if err := validateConfig(&cfg); err != nil {
		return nil, err // Error is already wrapped appropriately
	}

	// Parse command line flags last to override file config
	if err := ParseFlags(&cfg); err != nil {
		return nil, err // Error is already wrapped appropriately
	}

	return &cfg, nil
}

func LoadInitialLogging() (*LoggingConfig, error) {
	viper.Reset()
	setDefaults()

	if err := viper.ReadInConfig(); err != nil {
		var configFileNotFound viper.ConfigFileNotFoundError
		if errors.As(err, &configFileNotFound) {
			return &LoggingConfig{
				Environment: getEnvOrDefault("BUMPA_ENVIRONMENT", "development"),
				TimeFormat:  TimeFormatRFC3339,
				Output:      "console",
				Level:       getEnvOrDefault("BUMPA_LOG_LEVEL", "info"),
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

	// Validate tool prompts
	for i := range cfg.Tools {
		if strings.TrimSpace(cfg.Tools[i].SystemPrompt) == "" {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				errors.FormatContext(errors.ContextMissingPrompt, "system", cfg.Tools[i].Name),
			)
		}
		if strings.TrimSpace(cfg.Tools[i].UserPrompt) == "" {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				errors.FormatContext(errors.ContextMissingPrompt, "user", cfg.Tools[i].Name),
			)
		}
	}

	// Validate required tools exist
	if !hasRequiredTools(cfg.Tools) {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			errors.ContextMissingToolConfig,
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

func ParseFlags(cfg *Config) error {
	flagSet := flag.NewFlagSet("bumpa", flag.ExitOnError)
	command := flagSet.String("command", "", "The command to execute (commit, version, changelog, etc.)")

	if err := flagSet.Parse(os.Args[1:]); err != nil {
		return errors.Wrap(errors.CodeInputError, err)
	}

	if flagSet.NArg() > 0 {
		cfg.Command = flagSet.Arg(0)
	} else if *command != "" {
		cfg.Command = *command
	}

	if cfg.Command == "" {
		return errors.WrapWithContext(
			errors.CodeInputError,
			errors.ErrInvalidInput,
			errors.ContextNoCommand,
		)
	}

	logger.Debug().
		Str("command", cfg.Command).
		Int("numArgs", flagSet.NArg()).
		Msg("Parsed command")

	return nil
}

func setDefaults() {
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
	viper.SetDefault("logging.file_perms", DefaultLogFilePerms)
	viper.SetDefault("logging.dir_perms", DefaultLogDirPerms)
	viper.SetDefault("git.include_gitignore", true)
	viper.SetDefault("git.max_diff_lines", DefaultMaxDiffLines)
	viper.SetDefault("git.preferred_line_length", DefaultLineLength)
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

func FindTool(tools []Tool, name string) *Tool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

func hasRequiredTools(tools []Tool) bool {
	required := map[string]bool{
		"generate_file_summary":   false,
		"generate_commit_message": false,
	}

	for i := range tools {
		if _, ok := required[tools[i].Name]; ok {
			required[tools[i].Name] = true
		}
	}

	for _, found := range required {
		if !found {
			return false
		}
	}

	return true
}
