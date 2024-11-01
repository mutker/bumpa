package logger

import (
	"io"
	"os"
	"strings"

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

	logLevel := GetLogLevel(level)

	zerolog.SetGlobalLevel(logLevel)
	logger = zerolog.New(consoleWriter).With().
		Timestamp().
		Str("environment", environment).
		Logger()

	return nil
}

func SetLogger(l *zerolog.Logger) {
	if l != nil {
		logger = *l
	}
}

func GetLogger() zerolog.Logger {
	return logger
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

func GetLogLevel(level string) zerolog.Level {
	// zerolog levels are in reverse order (lower is more verbose):
	// trace(-1) -> debug(0) -> info(1) -> warn(2) -> error(3) -> fatal(4) -> panic(5)
	// Setting warn means "show warn and above (error, fatal)"
	// Setting info means "show info and above (warn, error, fatal)"
	switch strings.ToLower(level) {
	case "trace":
		return zerolog.TraceLevel // -1: most verbose
	case "debug":
		return zerolog.DebugLevel // 0: very verbose
	case "info":
		return zerolog.InfoLevel // 1: normal verbosity
	case "warn":
		return zerolog.WarnLevel // 2: warnings only
	case "error":
		return zerolog.ErrorLevel // 3: errors only
	case "fatal":
		return zerolog.FatalLevel // 4: fatal only
	default:
		return zerolog.InfoLevel // default to info
	}
}

func SetLogLevel(level LogLevel) {
	zerolog.SetGlobalLevel(zerolog.Level(level))
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
