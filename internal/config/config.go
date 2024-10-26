package config

import (
	"encoding/json"
	"flag"
	"os"
	"time"

	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/logger"
	"github.com/spf13/viper"
)

const (
	DefaultMaxRetries       = 3
	DefaultMaxDiffLines     = 10
	DefaultCommitMsgTimeout = 30 * time.Second
	DefaultLogFilePerms     = 0o666
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
	IncludeGitignore bool     `mapstructure:"include_gitignore"`
	Ignore           []string `mapstructure:"ignore"`
	MaxDiffLines     int      `mapstructure:"max_diff_lines"`
}

type LLMConfig struct {
	Provider         string
	Model            string
	BaseURL          string        `mapstructure:"base_url"`
	APIKey           string        `mapstructure:"api_key"`
	MaxRetries       int           `mapstructure:"max_retries"`
	CommitMsgTimeout time.Duration `mapstructure:"commit_msg_timeout"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

//nolint:wrapcheck // Using WrapWithContext for configuration-specific error context
func Load() (*Config, error) {
	viper.SetConfigName(".bumpa")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

	setDefaults()

	if err := viper.ReadInConfig(); err != nil {
		if !errors.IsConfigFileNotFound(err) {
			return nil, errors.WrapWithContext(errors.CodeConfigError, err, "failed to read config file")
		}
		// Config file not found; continue with defaults
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, errors.WrapWithContext(errors.CodeConfigError, err, "failed to unmarshal config")
	}

	if err := ParseFlags(&cfg); err != nil {
		return nil, errors.WrapWithContext(errors.CodeInputError, err, "failed to parse flags")
	}

	if err := logger.Init(cfg.Logging.Environment, cfg.Logging.TimeFormat,
		cfg.Logging.Output, cfg.Logging.Level, cfg.Logging.Path); err != nil {
		return nil, errors.WrapWithContext(errors.CodeInitFailed, err, "failed to initialize logger")
	}

	return &cfg, nil
}

func setDefaults() {
	viper.SetDefault("llm.provider", "ollama")
	viper.SetDefault("llm.model", "llama3.2:latest")
	viper.SetDefault("llm.base_url", "http://localhost:11434")
	viper.SetDefault("llm.api_key", "")
	viper.SetDefault("llm.max_retries", DefaultMaxRetries)
	viper.SetDefault("llm.commit_msg_timeout", DefaultCommitMsgTimeout)
	viper.SetDefault("logging.environment", "development")
	viper.SetDefault("logging.timeformat", time.RFC3339)
	viper.SetDefault("logging.output", "console")
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.file_perms", DefaultLogFilePerms)
	viper.SetDefault("git.include_gitignore", true)
	viper.SetDefault("git.max_diff_lines", DefaultMaxDiffLines)
}

func ParseFlags(cfg *Config) error {
	flagSet := flag.NewFlagSet("bumpa", flag.ExitOnError)
	flagSet.StringVar(&cfg.Command, "command", "", "The command to execute (commit, version, changelog, etc.)")

	err := flagSet.Parse(os.Args[1:])
	if err != nil {
		return errors.Wrap(errors.CodeInputError, err)
	}

	return nil
}
