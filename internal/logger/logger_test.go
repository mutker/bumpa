package logger

import (
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

const LogLevelHelpText = `
Logging Levels (from most to least verbose):
- trace: logs everything
- debug: debug + info + warn + error + fatal
- info:  info + warn + error + fatal
- warn:  warn + error + fatal
- error: error + fatal
- fatal: fatal only
`

func TestZerologLevelBehavior(t *testing.T) {
	tests := []struct {
		name       string
		setLevel   zerolog.Level
		logLevel   zerolog.Level
		shouldShow bool
	}{
		// When level is set to WARN (2)
		{"Warn Shows Error", zerolog.WarnLevel, zerolog.ErrorLevel, true},  // 2 shows 3
		{"Warn Shows Warn", zerolog.WarnLevel, zerolog.WarnLevel, true},    // 2 shows 2
		{"Warn Hides Info", zerolog.WarnLevel, zerolog.InfoLevel, false},   // 2 hides 1
		{"Warn Hides Debug", zerolog.WarnLevel, zerolog.DebugLevel, false}, // 2 hides 0

		// When level is set to INFO (1)
		{"Info Shows Error", zerolog.InfoLevel, zerolog.ErrorLevel, true},  // 1 shows 3
		{"Info Shows Warn", zerolog.InfoLevel, zerolog.WarnLevel, true},    // 1 shows 2
		{"Info Shows Info", zerolog.InfoLevel, zerolog.InfoLevel, true},    // 1 shows 1
		{"Info Hides Debug", zerolog.InfoLevel, zerolog.DebugLevel, false}, // 1 hides 0
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			logger := zerolog.New(&buf)
			zerolog.SetGlobalLevel(tt.setLevel)

			logger.WithLevel(tt.logLevel).Msg("test")

			hasOutput := buf.Len() > 0
			if hasOutput != tt.shouldShow {
				t.Errorf("Set level %v, log level %v: expected show=%v, got show=%v",
					tt.setLevel, tt.logLevel, tt.shouldShow, hasOutput)
			}
		})
	}
}

func TestLogLevelEnforcement(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		inputLevel  string
		wantLevel   zerolog.Level
	}{
		{
			name:        "Development Debug",
			environment: "development",
			inputLevel:  "debug",
			wantLevel:   zerolog.DebugLevel,
		},
		{
			name:        "Production Forces Info",
			environment: "production",
			inputLevel:  "debug",
			wantLevel:   zerolog.InfoLevel,
		},
		{
			name:        "Production Allows Warning",
			environment: "production",
			inputLevel:  "warn",
			wantLevel:   zerolog.WarnLevel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Init(tt.environment, "RFC3339", "console", tt.inputLevel, "")
			if err != nil {
				t.Fatalf("Init() error = %v", err)
			}

			got := zerolog.GlobalLevel()
			if got != tt.wantLevel {
				t.Errorf("Log level = %v, want %v", got, tt.wantLevel)
			}
		})
	}
}

func TestLogLevelHierarchy(t *testing.T) {
	tests := []struct {
		configLevel string
		messages    []struct {
			level      string
			shouldShow bool
		}
	}{
		{
			configLevel: "warn",
			messages: []struct {
				level      string
				shouldShow bool
			}{
				{"debug", false},
				{"info", false},
				{"warn", true},
				{"error", true},
				{"fatal", true},
			},
		},
		{
			configLevel: "info",
			messages: []struct {
				level      string
				shouldShow bool
			}{
				{"debug", false},
				{"info", true},
				{"warn", true},
				{"error", true},
				{"fatal", true},
			},
		},
	}

	for _, tt := range tests {
		t.Run("Config Level "+tt.configLevel, func(t *testing.T) {
			var buf strings.Builder
			consoleWriter := zerolog.ConsoleWriter{
				Out:        &buf,
				TimeFormat: "15:04:05",
			}

			// Initialize logger with test level
			zerolog.SetGlobalLevel(getLogLevel(tt.configLevel))
			logger = zerolog.New(consoleWriter).With().Logger()

			// Test each message level
			for _, msg := range tt.messages {
				buf.Reset()
				logEvent := logger.WithLevel(getLogLevel(msg.level))
				logEvent.Msg("test")

				hasOutput := buf.String() != ""
				if hasOutput != msg.shouldShow {
					t.Errorf("Level %s with config %s: expected show=%v, got output: %q",
						msg.level, tt.configLevel, msg.shouldShow, buf.String())
				}
			}
		})
	}
}

func TestProductionLogLevels(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		level       string
		shouldLog   bool
		logFunc     func() *zerolog.Event
	}{
		{
			name:        "Production Warn Shows Warn",
			environment: "production",
			level:       "warn",
			shouldLog:   true,
			logFunc:     Warn,
		},
		{
			name:        "Production Warn Hides Info",
			environment: "production",
			level:       "warn",
			shouldLog:   false,
			logFunc:     Info,
		},
		{
			name:        "Production Warn Shows Error",
			environment: "production",
			level:       "warn",
			shouldLog:   true,
			logFunc:     Error,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a buffer for capturing output
			var buf strings.Builder

			// Create console writer with our buffer
			consoleWriter := zerolog.ConsoleWriter{
				Out:        &buf,
				TimeFormat: time.RFC3339,
			}

			// Set up logger directly instead of using Init
			zerolog.SetGlobalLevel(getLogLevel(tt.level))
			logger = zerolog.New(consoleWriter).With().
				Timestamp().
				Str("environment", tt.environment).
				Logger()

			// Try logging
			tt.logFunc().Msg("test message")

			// Check if we got output
			gotOutput := buf.String() != ""
			if gotOutput != tt.shouldLog {
				t.Errorf("Expected log output: %v, got output: %v\nBuffer: %q",
					tt.shouldLog, gotOutput, buf.String())
			}
		})
	}
}

// Add helper function for testing
func CaptureOutput(t *testing.T, fn func()) string {
	var buf strings.Builder

	// Store original logger
	originalLogger := logger

	// Create test logger
	consoleWriter := zerolog.ConsoleWriter{
		Out:        &buf,
		TimeFormat: time.RFC3339,
	}
	logger = zerolog.New(consoleWriter).With().Timestamp().Logger()

	// Reset logger after test
	defer func() {
		logger = originalLogger
	}()

	// Run the test function
	fn()

	return buf.String()
}

// Add more comprehensive level tests
func TestLogLevelFiltering(t *testing.T) {
	tests := []struct {
		name        string
		level       string
		logFunc     func() *zerolog.Event
		shouldLog   bool
		msgContains string
	}{
		{
			name:        "Warn Level Shows Error",
			level:       "warn",
			logFunc:     Error,
			shouldLog:   true,
			msgContains: "ERROR",
		},
		{
			name:        "Warn Level Shows Warn",
			level:       "warn",
			logFunc:     Warn,
			shouldLog:   true,
			msgContains: "WARN",
		},
		{
			name:        "Warn Level Hides Info",
			level:       "warn",
			logFunc:     Info,
			shouldLog:   false,
			msgContains: "",
		},
		{
			name:        "Info Level Shows Warn",
			level:       "info",
			logFunc:     Warn,
			shouldLog:   true,
			msgContains: "WARN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := CaptureOutput(t, func() {
				if err := Init("test", time.RFC3339, "console", tt.level, ""); err != nil {
					t.Fatalf("Init failed: %v", err)
				}
				tt.logFunc().Msg("test message")
			})

			hasOutput := output != ""
			if hasOutput != tt.shouldLog {
				t.Errorf("Expected output: %v, got: %v\nOutput: %q",
					tt.shouldLog, hasOutput, output)
			}

			if tt.shouldLog && !strings.Contains(output, tt.msgContains) {
				t.Errorf("Expected output to contain %q, got: %q",
					tt.msgContains, output)
			}
		})
	}
}

func ShouldLog(messageLevel, configLevel zerolog.Level) bool {
	// In zerolog, lower level numbers are more verbose
	// So we want to log if the message level is >= config level
	return messageLevel >= configLevel
}
