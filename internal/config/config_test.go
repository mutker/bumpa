package config_test

import (
	"os"
	"strings"
	"testing"

	"codeberg.org/mutker/bumpa/internal/config"
	"github.com/spf13/viper"
)

func TestLogLevelOverride(t *testing.T) {
	tests := []struct {
		name      string
		envLevel  string
		envName   string
		wantLevel string
		wantEnv   string
	}{
		{
			name:      "Default Level",
			envLevel:  "",
			wantLevel: "info",
			wantEnv:   "development",
		},
		{
			name:      "Debug Override in Development",
			envLevel:  "debug",
			envName:   "development",
			wantLevel: "debug",
			wantEnv:   "development",
		},
		{
			name:      "Debug in Production",
			envLevel:  "debug",
			envName:   "production",
			wantLevel: "debug", // Raw config value should be debug
			wantEnv:   "production",
		},
		{
			name:      "Warning Level",
			envLevel:  "warn",
			wantLevel: "warn",
			wantEnv:   "development",
		},
		{
			name:      "Error Level",
			envLevel:  "error",
			wantLevel: "error",
			wantEnv:   "development",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset environment and viper
			os.Clearenv()
			viper.Reset()

			// Set test environment
			if tt.envLevel != "" {
				t.Setenv("BUMPA_LOG_LEVEL", tt.envLevel)
			}
			if tt.envName != "" {
				t.Setenv("BUMPA_ENVIRONMENT", tt.envName)
			}

			// Load config
			cfg, err := config.LoadInitialLogging()
			if err != nil {
				t.Fatalf("LoadInitialLogging() error = %v", err)
			}

			// Check results
			if got := cfg.Level; got != tt.wantLevel {
				t.Errorf("LoadInitialLogging() level = %v, want %v", got, tt.wantLevel)
			}

			// Verify environment
			if got := cfg.Environment; got != tt.wantEnv {
				t.Errorf("LoadInitialLogging() environment = %v, want %v", got, tt.wantEnv)
			}
		})
	}
}

func TestEnvironmentConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  config.EnvironmentConfig
		wantErr bool
	}{
		{
			name: "Valid Development Config",
			config: config.EnvironmentConfig{
				Name:       "development",
				TimeFormat: "2006-01-02T15:04:05Z07:00",
				Output:     "console",
				Level:      "debug",
			},
			wantErr: false,
		},
		{
			name: "Valid Production Config",
			config: config.EnvironmentConfig{
				Name:       "production",
				TimeFormat: "2006-01-02 15:04:05",
				Output:     "file",
				Level:      "info",
				Path:       "config_test.log",
				FilePerms:  0o644,
			},
			wantErr: false,
		},
		{
			name: "Invalid Time Format",
			config: config.EnvironmentConfig{
				TimeFormat: "invalid",
			},
			wantErr: true,
		},
		{
			name: "Missing File Path",
			config: config.EnvironmentConfig{
				Output: "file",
			},
			wantErr: true,
		},
		{
			name: "Invalid Log Level",
			config: config.EnvironmentConfig{
				Level: "invalid",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("EnvironmentConfig.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEnvironmentVariableOverrides(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		validate func(*config.Config) bool
	}{
		{
			name: "Debug Mode",
			envVars: map[string]string{
				"BUMPA_DEBUG": "true",
			},
			validate: func(cfg *config.Config) bool {
				return cfg.Logging.Level == "debug"
			},
		},
		{
			name: "Custom Log Level",
			envVars: map[string]string{
				"BUMPA_LOG_LEVEL": "warn",
			},
			validate: func(cfg *config.Config) bool {
				return cfg.Logging.Level == "warn"
			},
		},
		{
			name: "Custom Environment",
			envVars: map[string]string{
				"BUMPA_ENVIRONMENT": "production",
			},
			validate: func(cfg *config.Config) bool {
				return cfg.Logging.Environment == "production"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset environment
			os.Clearenv()

			// Set test environment variables
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			// Load config
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			// Validate result
			if !tt.validate(cfg) {
				t.Errorf("Configuration not properly overridden by environment variables")
			}
		})
	}
}

// Add helper function to check log levels
func IsValidLogLevel(level string) bool {
	switch strings.ToLower(level) {
	case "trace", "debug", "info", "warn", "error", "fatal", "panic":
		return true
	default:
		return false
	}
}
