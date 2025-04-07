// Package logger provides a logging capability for toolhive for running locally as a CLI and in Kubernetes
package logger

import (
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/lmittmann/tint"
)

// Log is a global logger instance
var Log Logger

// Logger provides a unified interface for logging
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Implementation using slog
type slogLogger struct {
	logger *slog.Logger
}

func (l *slogLogger) Debug(msg string, args ...any) {
	l.logger.Debug(msg, args...)
}

func (l *slogLogger) Info(msg string, args ...any) {
	l.logger.Info(msg, args...)
}

func (l *slogLogger) Warn(msg string, args ...any) {
	l.logger.Warn(msg, args...)
}

func (l *slogLogger) Error(msg string, args ...any) {
	l.logger.Error(msg, args...)
}

func unstructuredLogs() bool {
	unstructuredLogs, err := strconv.ParseBool(os.Getenv("UNSTRUCTURED_LOGS"))
	if err != nil {
		// at this point if the error is not nil, the env var wasn't set, or is ""
		// which means we just default to outputting unstructured logs.
		return true
	}
	return unstructuredLogs
}

// Initialize creates and configures the appropriate logger.
// If the UNSTRUCTURED_LOGS is set to true, it will output plain log message
// with only time and LogLevelType (INFO, DEBUG, ERROR, WARN)).
// Otherwise it will create a standard structured slog logger
func Initialize() {
	if unstructuredLogs() {
		w := os.Stderr

		logger := slog.New(tint.NewHandler(w, nil))

		// set global logger with custom options
		slog.SetDefault(slog.New(
			tint.NewHandler(w, &tint.Options{
				// TODO: we should probably set the below based on a flag passed to CLI
				Level:      slog.LevelDebug,
				TimeFormat: time.Kitchen,
			}),
		))

		Log = logger
	} else {
		handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			// TODO: we should probably set the below based on a flag passed to CLI
			Level: slog.LevelDebug,
		})
		slogger := slog.New(handler)
		Log = &slogLogger{logger: slogger}

		// Also set as default slog logger
		slog.SetDefault(slogger)
	}
}

// GetLogger returns a context-specific logger
func GetLogger(component string) Logger {
	if slogger, ok := Log.(*slogLogger); ok {
		return &slogLogger{
			logger: slogger.logger.With("component", component),
		}
	}

	return Log
}
