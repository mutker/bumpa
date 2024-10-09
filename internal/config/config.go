package config

import (
	"encoding/json"
	"fmt"
	"time"

	"codeberg.org/mutker/bumpa/pkg/logger"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

type Config struct {
	Logging LoggingConfig
	Git     GitConfig
	LLM     LLMConfig
	Tools   []Tool `mapstructure:"tools"`
}

type LoggingConfig struct {
	Environment string
	TimeFormat  string
	Output      string
	Level       string
	Path        string `mapstructure:"file_path"`
}

type GitConfig struct {
	IncludeGitignore bool     `mapstructure:"include_gitignore"`
	Ignore           []string `mapstructure:"ignore"`
}

type LLMConfig struct {
	Provider   string
	Model      string
	BaseURL    string `mapstructure:"base_url"`
	APIKey     string `mapstructure:"api_key"`
	MaxRetries int    `mapstructure:"max_retries"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func Load() (*Config, error) {
	viper.SetConfigName(".bumpa")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

	setDefaults()

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
		// Don't log here, as the logger hasn't been initialized yet
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unable to decode into config struct: %w", err)
	}

	// Initialize logger
	if err := logger.Init(logger.Config{
		Environment: cfg.Logging.Environment,
		TimeFormat:  cfg.Logging.TimeFormat,
		Output:      cfg.Logging.Output,
		Level:       cfg.Logging.Level,
		Path:        cfg.Logging.Path,
	}); err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %w", err)
	}

	// Now we can use the logger
	if _, ok := viper.ReadInConfig().(viper.ConfigFileNotFoundError); ok {
		log.Warn().Msg("No configuration file found, using defaults and environment variables")
	}

	return &cfg, nil
}

func setDefaults() {
	viper.SetDefault("llm.provider", "ollama")
	viper.SetDefault("llm.model", "llama3.2:latest")
	viper.SetDefault("llm.base_url", "http://localhost:11434")
	viper.SetDefault("llm.api_key", "")
	viper.SetDefault("llm.max_retries", 3)
	viper.SetDefault("logging.environment", "development")
	viper.SetDefault("logging.timeformat", time.RFC3339)
	viper.SetDefault("logging.output", "console")
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("git.include_gitignore", true)
}
