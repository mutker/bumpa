package logger

import (
	"io"
	"os"
	"syscall"

	"codeberg.org/mutker/bumpa/internal/errors"
	"github.com/rs/zerolog"
)

var logger zerolog.Logger

type LogLevel int8

const (
	DebugLevel LogLevel = iota
	InfoLevel
	WarnLevel
	ErrorLevel
	FatalLevel
	logFilePermissions = 0o666
)

func Init(environment, timeFormat, output, level, path string) error {
	writer, err := getWriter(output, path)
	if err != nil {
		return err
	}

	consoleWriter := zerolog.ConsoleWriter{
		Out:        writer,
		TimeFormat: timeFormat,
	}

	logLevel := getLogLevel(level)
	logLevel = adjustLogLevelForEnvironment(environment, logLevel)

	zerolog.SetGlobalLevel(logLevel)
	logger = zerolog.New(consoleWriter).With().Timestamp().Str("environment", environment).Logger()

	return nil
}

func getWriter(output, path string) (io.Writer, error) {
	if output == "file" && path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFilePermissions)
		if err != nil {
			return nil, errors.Wrap(errors.CodeInitFailed, err)
		}

		return file, nil
	}

	return os.Stdout, nil
}

func adjustLogLevelForEnvironment(environment string, logLevel zerolog.Level) zerolog.Level {
	switch environment {
	case "development":
		return zerolog.DebugLevel
	case "production":
		if logLevel < zerolog.InfoLevel {
			return zerolog.InfoLevel
		}
	}

	return logLevel
}

func getLogLevel(level string) zerolog.Level {
	switch level {
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}

func SetLogLevel(level LogLevel) {
	zerolog.SetGlobalLevel(zerolog.Level(level))
}

// IsService checks if the application is running as a service
func IsService() bool {
	if _, err := os.Stdin.Stat(); err != nil {
		return true
	}
	if os.Getenv("SERVICE_NAME") != "" || os.Getenv("INVOCATION_ID") != "" {
		return true
	}
	if os.Getppid() == 1 {
		return true
	}

	return syscall.Getpgrp() == syscall.Getpid()
}

// Debug logs a debug message
func Debug() *zerolog.Event {
	return logger.Debug()
}

func DebugWithComponent(component string) *zerolog.Event {
	return logger.Debug().Str("component", component)
}

// Info logs an info message
func Info() *zerolog.Event {
	return logger.Info()
}

// Warn logs a warning message
func Warn() *zerolog.Event {
	return logger.Warn()
}

// Error logs an error message
func Error() *zerolog.Event {
	return logger.Error()
}

// ErrorWithCode logs an error message and returns a wrapped error
func ErrorWithCode(code string, err error, msg string) error {
	wrappedErr := errors.Wrap(code, err)
	Error().
		Str("error_code", code).
		Str("error_message", msg).
		AnErr("error", err).
		Msg("An error occurred")

	return wrappedErr
}

// Fatal logs a fatal message and exits the program
func Fatal() *zerolog.Event {
	return logger.Fatal()
}
