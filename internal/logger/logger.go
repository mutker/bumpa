package logger

import (
	"io"
	"os"
	"strings"
	"time"

	"codeberg.org/mutker/bumpa/internal/errors"
	"github.com/rs/zerolog"
)

// LogEvent represents a logging event that can be chained
type LogEvent interface {
	Str(key string, value string) LogEvent
	Int(key string, value int) LogEvent
	Bool(key string, value bool) LogEvent
	Float64(key string, value float64) LogEvent
	Err(err error) LogEvent
	Interface(key string, value interface{}) LogEvent
	Time(key string, value time.Time) LogEvent
	Dur(key string, value time.Duration) LogEvent
	Msg(msg string)
	Msgf(format string, v ...interface{})
}

// Config holds logger configuration
type Config struct {
	Environment string
	TimeFormat  string
	Output      string
	Level       string
	Path        string
	FilePerms   os.FileMode
}

var (
	defaultLogger zerolog.Logger
	isInitialized bool
)

// zerologEvent adapts zerolog.Event to our LogEvent interface
type zerologEvent struct {
	event *zerolog.Event
}

func (e *zerologEvent) Str(key, value string) LogEvent {
	e.event.Str(key, value)
	return e
}

func (e *zerologEvent) Int(key string, value int) LogEvent {
	e.event.Int(key, value)
	return e
}

func (e *zerologEvent) Float64(key string, value float64) LogEvent {
	e.event.Float64(key, value)
	return e
}

func (e *zerologEvent) Bool(key string, value bool) LogEvent {
	e.event.Bool(key, value)
	return e
}

func (e *zerologEvent) Err(err error) LogEvent {
	e.event.Err(err)
	return e
}

func (e *zerologEvent) Interface(key string, value interface{}) LogEvent {
	e.event.Interface(key, value)
	return e
}

func (e *zerologEvent) Time(key string, value time.Time) LogEvent {
	e.event.Time(key, value)
	return e
}

func (e *zerologEvent) Dur(key string, value time.Duration) LogEvent {
	e.event.Dur(key, value)
	return e
}

func (e *zerologEvent) Msg(msg string) {
	e.event.Msg(msg)
}

func (e *zerologEvent) Msgf(format string, v ...interface{}) {
	e.event.Msgf(format, v...)
}

// Global functions for logging
func Debug() LogEvent { return &zerologEvent{event: defaultLogger.Debug()} }
func Info() LogEvent  { return &zerologEvent{event: defaultLogger.Info()} }
func Warn() LogEvent  { return &zerologEvent{event: defaultLogger.Warn()} }
func Error() LogEvent { return &zerologEvent{event: defaultLogger.Error()} }
func Fatal() LogEvent { return &zerologEvent{event: defaultLogger.Fatal()} }

// Init initializes the logger with the given configuration
//
//nolint:gocritic // Accepting value type for simpler API
func Init(cfg Config) error {
	if cfg.TimeFormat == "" {
		cfg.TimeFormat = "2006-01-02T15:04:05Z07:00"
	}

	zerolog.TimeFieldFormat = cfg.TimeFormat

	var output io.Writer
	if cfg.Output == "file" && cfg.Path != "" {
		file, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, cfg.FilePerms)
		if err != nil {
			return errors.WrapWithContext(
				errors.CodeConfigError,
				err,
				errors.FormatContext(errors.ContextFileCreate, cfg.Path),
			)
		}
		output = file
	} else {
		output = zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: cfg.TimeFormat,
			NoColor:    false,
		}
	}

	level, err := zerolog.ParseLevel(strings.ToLower(cfg.Level))
	if err != nil {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			errors.ContextInvalidLogLevel,
		)
	}
	zerolog.SetGlobalLevel(level)

	defaultLogger = zerolog.New(output).With().Timestamp().Logger()
	isInitialized = true
	return nil
}

// IsInitialized returns whether the logger has been initialized
func IsInitialized() bool {
	return isInitialized
}
