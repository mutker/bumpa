package logger

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Config struct {
	Environment string
	TimeFormat  string
	Output      string
	Level       string
	Path        string
}

var Logger zerolog.Logger

func Init(cfg Config) error {
	level, err := zerolog.ParseLevel(cfg.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}

	timeFormat := getTimeFormat(cfg.TimeFormat)

	var output io.Writer
	switch cfg.Output {
	case "file":
		if cfg.Path == "" {
			return fmt.Errorf("log file path is not set")
		}
		file, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		output = file
	case "journald":
		// Implement journald setup if needed
		return fmt.Errorf("journald logging not implemented")
	default:
		output = os.Stdout
	}

	logger := zerolog.New(output).Level(level).With().Timestamp().Logger()

	if cfg.Environment != "production" {
		logger = logger.Output(zerolog.ConsoleWriter{
			Out:        output,
			TimeFormat: timeFormat,
			NoColor:    false,
		})
	}

	log.Logger = logger

	return nil
}

func getTimeFormat(formatString string) string {
	switch formatString {
	case "RFC3339":
		return time.RFC3339
	case "TimeFormatUnix":
		return "UNIXTIME"
	default:
		return time.RFC3339 // Default to RFC3339 if not specified or unknown
	}
}
