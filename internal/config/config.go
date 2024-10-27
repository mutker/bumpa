package config

import (
	"flag"
	"os"
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
	DefaultLogFilePerms     = 0o666
	DefaultLineLength       = 72
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
	Environment string
	TimeFormat  string
	Output      string
	Level       string
	Path        string `mapstructure:"file_path"`
	FilePerms   int    `mapstructure:"file_perms"`
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
	viper.SetConfigName(".bumpa")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

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

	logger.Debug().
		Str("provider", cfg.LLM.Provider).
		Str("base_url", cfg.LLM.BaseURL).
		Str("model", cfg.LLM.Model).
		Msg("Loaded LLM configuration")

	// Add validation for tool prompts
	for i := range cfg.Tools {
		if strings.TrimSpace(cfg.Tools[i].SystemPrompt) == "" { // Field name remains camelCase in Go code
			return nil, errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				"missing system prompt for tool: "+cfg.Tools[i].Name,
			)
		}
		if strings.TrimSpace(cfg.Tools[i].UserPrompt) == "" { // Field name remains camelCase in Go code
			return nil, errors.WrapWithContext(
				errors.CodeConfigError,
				errors.ErrInvalidInput,
				"missing user prompt for tool: "+cfg.Tools[i].Name,
			)
		}
	}

	// Validate required tools exist
	if !hasRequiredTools(cfg.Tools) {
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"missing required tools configuration",
		)
	}

	if err := ParseFlags(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
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
	viper.SetDefault("logging.timeformat", time.RFC3339)
	viper.SetDefault("logging.output", "console")
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.file_perms", DefaultLogFilePerms)
	viper.SetDefault("git.include_gitignore", true)
	viper.SetDefault("git.max_diff_lines", DefaultMaxDiffLines)
	viper.SetDefault("git.preferred_line_length", DefaultLineLength)
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
