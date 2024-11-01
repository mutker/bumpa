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

type LoggingConfig struct {
	// Current environment name
	Env string `mapstructure:"env"`
	// Backward compatibility fields
	Environment  string              `mapstructure:"environment,omitempty"`
	TimeFormat   string              `mapstructure:"timeformat,omitempty"`
	Output       string              `mapstructure:"output,omitempty"`
	Level        string              `mapstructure:"level,omitempty"`
	Path         string              `mapstructure:"file_path,omitempty"`
	FilePerms    os.FileMode         `mapstructure:"file_perms,omitempty"`
	DirPerms     os.FileMode         `mapstructure:"dir_perms,omitempty"`
	Environments []EnvironmentConfig `mapstructure:"environments,omitempty"`
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
		return nil, errors.Wrap(errors.CodeConfigError, err)
	}
	if err := viper.BindEnv("logging.environment", "BUMPA_ENVIRONMENT"); err != nil {
		return nil, errors.Wrap(errors.CodeConfigError, err)
	}

	// Enable environment variables
	viper.AutomaticEnv()
	viper.SetEnvPrefix("BUMPA")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	setDefaults()

	if err := viper.ReadInConfig(); err != nil {
		var configFileNotFound viper.ConfigFileNotFoundError
		if errors.As(err, &configFileNotFound) {
			logger.Warn().Msg("No config file found, using defaults")
		} else {
			return nil, errors.WrapWithContext(
				errors.CodeConfigError,
				err,
				"failed to read config file",
			)
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			err,
			"failed to unmarshal config",
		)
	}

	// Validate configuration
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}

	// Parse command line flags last to override file config
	if err := ParseFlags(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func LoadInitialLogging() (*LoggingConfig, error) {
	// Reset viper state
	viper.Reset()

	// Set up viper for initial config load
	viper.SetConfigName(".bumpa")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	// Bind environment variables first
	if err := viper.BindEnv("logging.level", "BUMPA_LOG_LEVEL"); err != nil {
		return nil, errors.Wrap(errors.CodeConfigError, err)
	}
	if err := viper.BindEnv("logging.environment", "BUMPA_ENVIRONMENT"); err != nil {
		return nil, errors.Wrap(errors.CodeConfigError, err)
	}

	// Enable environment variables
	viper.AutomaticEnv()
	viper.SetEnvPrefix("BUMPA")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Set logging defaults (only if env vars are not set)
	if viper.GetString("logging.level") == "" {
		viper.SetDefault("logging.level", "info")
	}
	if viper.GetString("logging.environment") == "" {
		viper.SetDefault("logging.environment", "development")
	}
	viper.SetDefault("logging.timeformat", time.RFC3339)
	viper.SetDefault("logging.output", "console")
	viper.SetDefault("logging.file_perms", DefaultLogFilePerms)

	// Try to read config file
	if err := viper.ReadInConfig(); err != nil {
		var configFileNotFound viper.ConfigFileNotFoundError
		if errors.As(err, &configFileNotFound) {
			// Return current config (with env vars and defaults)
			return &LoggingConfig{
				Environment: viper.GetString("logging.environment"),
				TimeFormat:  viper.GetString("logging.timeformat"),
				Output:      viper.GetString("logging.output"),
				Level:       viper.GetString("logging.level"),
				FilePerms:   safeFileMode(viper.GetInt("logging.file_perms")),
			}, nil
		}
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			err,
			"failed to read config file",
		)
	}

	var cfg struct {
		Logging LoggingConfig `mapstructure:"logging"`
	}

	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			err,
			"failed to unmarshal logging config",
		)
	}

	// Get active environment configuration
	active := cfg.Logging.ActiveEnvironment()

	// Environment variables should only override if explicitly set
	if envLevel := os.Getenv("BUMPA_LOG_LEVEL"); envLevel != "" {
		active.Level = envLevel
	}

	return &LoggingConfig{
		Env:         cfg.Logging.Env,
		Environment: active.Name,
		TimeFormat:  active.TimeFormat,
		Output:      active.Output,
		Level:       active.Level,
		Path:        active.Path,
		FilePerms:   active.FilePerms,
		DirPerms:    active.DirPerms,
	}, nil
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

func validateConfig(cfg *Config) error {
	// Validate tool prompts
	for i := range cfg.Tools {
		if strings.TrimSpace(cfg.Tools[i].SystemPrompt) == "" {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				"missing system prompt for tool: "+cfg.Tools[i].Name,
			)
		}
		if strings.TrimSpace(cfg.Tools[i].UserPrompt) == "" {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				"missing user prompt for tool: "+cfg.Tools[i].Name,
			)
		}
		// Environment variables take precedence over config file
		if envLevel := os.Getenv("BUMPA_LOG_LEVEL"); envLevel != "" {
			viper.SetDefault("logging.level", envLevel)
		}
		if envEnv := os.Getenv("BUMPA_ENVIRONMENT"); envEnv != "" {
			viper.SetDefault("logging.environment", envEnv)
		}

		// Add environment variable bindings
		if err := viper.BindEnv("logging.level", "BUMPA_LOG_LEVEL"); err != nil {
			return errors.Wrap(errors.CodeConfigError, err)
		}
		if err := viper.BindEnv("logging.environment", "BUMPA_ENVIRONMENT"); err != nil {
			return errors.Wrap(errors.CodeConfigError, err)
		}
	}

	// Validate required tools exist
	if !hasRequiredTools(cfg.Tools) {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"missing required tools configuration",
		)
	}

	return nil
}

// Helper function for safe permission conversion
//
//nolint:gosec // Safe conversion as we explicitly mask to valid permission bits
func safeFileMode(perms int) os.FileMode {
	return os.FileMode(perms & int(DefaultLogFilePerms))
}

func (c *EnvironmentConfig) Validate() error {
	// Validate time format by attempting to parse a known time string
	if c.TimeFormat != "" {
		// Use a reference time that includes all components
		referenceTime := time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC)

		// Try to format using the provided format string
		var formatErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					formatErr = errors.WrapWithContext(
						errors.CodeConfigError,
						errors.ErrInvalidInput,
						"invalid time format: "+c.TimeFormat,
					)
				}
			}()
			_ = referenceTime.Format(c.TimeFormat)
		}()

		if formatErr != nil {
			return formatErr
		}
	}

	// Validate output and file path combination
	if c.Output == "file" {
		if c.Path == "" {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				"file output requires file_path to be set",
			)
		}

		// Check if file path is writable
		dir := filepath.Dir(c.Path)
		if err := os.MkdirAll(dir, DefaultLogDirPerms); err != nil {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				err,
				"failed to create log directory: "+dir,
			)
		}

		// Try to open file
		perms := safeFileMode(int(c.FilePerms))
		f, err := os.OpenFile(c.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, perms)
		if err != nil {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				err,
				"failed to open log file: "+c.Path,
			)
		}
		f.Close()
	}

	// Validate log level
	if !isValidLogLevel(c.Level) {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"invalid log level: "+c.Level,
		)
	}

	return nil
}

func isValidLogLevel(level string) bool {
	switch strings.ToLower(level) {
	case "debug", "info", "warn", "error", "fatal":
		return true
	default:
		return false
	}
}

func (c *LoggingConfig) ActiveEnvironment() *EnvironmentConfig {
	// Check for environment variable override
	envName := os.Getenv("BUMPA_ENVIRONMENT")
	if envName == "" {
		// Use env field if set, fall back to environment field for compatibility
		envName = c.Env
		if envName == "" {
			envName = c.Environment
		}
	}

	// Look for matching environment config
	for i := range c.Environments {
		if c.Environments[i].Name == envName {
			return &c.Environments[i]
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

func ParseFlags(cfg *Config) error {
	flagSet := flag.NewFlagSet("bumpa", flag.ExitOnError)

	// Define the command flag with a default value
	command := flagSet.String("command", "", "The command to execute (commit, version, changelog, etc.)")

	// Parse remaining args after the command
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		return errors.Wrap(errors.CodeInputError, err)
	}

	// If command was passed as first argument without flag
	if flagSet.NArg() > 0 {
		cfg.Command = flagSet.Arg(0)
	} else if *command != "" {
		cfg.Command = *command
	}

	// Validate command is not empty
	if cfg.Command == "" {
		return errors.WrapWithContext(
			errors.CodeInputError,
			errors.ErrInvalidInput,
			"no command specified",
		)
	}

	logger.Debug().
		Str("command", cfg.Command).
		Int("numArgs", flagSet.NArg()).
		Msg("Parsed command")

	return nil
}
