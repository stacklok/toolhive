// Package logger provides a logging capability for toolhive for running locally as a CLI and in Kubernetes
package logger

import (
	"fmt"
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
	Debugf(msg string, args ...any)
	Info(msg string, args ...any)
	Infof(msg string, args ...any)
	Warn(msg string, args ...any)
	Warnf(msg string, args ...any)
	Error(msg string, args ...any)
	Errorf(msg string, args ...any)
}

// Implementation using slog
type slogLogger struct {
	logger *slog.Logger
}

func (l *slogLogger) Debugf(msg string, args ...any) {
	l.logger.Debug(fmt.Sprintf(msg, args...))
}

func (l *slogLogger) Infof(msg string, args ...any) {
	l.logger.Info(fmt.Sprintf(msg, args...))
}

func (l *slogLogger) Warnf(msg string, args ...any) {
	l.logger.Warn(fmt.Sprintf(msg, args...))
}

func (l *slogLogger) Errorf(msg string, args ...any) {
	l.logger.Error(fmt.Sprintf(msg, args...))
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

		handler := tint.NewHandler(w, &tint.Options{
			Level:      slog.LevelDebug,
			TimeFormat: time.Kitchen,
		})

		slogger := slog.New(handler)

		slog.SetDefault(slogger)
		Log = &slogLogger{logger: slogger}
	} else {
		w := os.Stdout

		handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
			// TODO: we should probably set the below based on a flag passed to CLI
			Level: slog.LevelDebug,
		})

		slogger := slog.New(handler)

		slog.SetDefault(slogger)
		Log = &slogLogger{logger: slogger}
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
