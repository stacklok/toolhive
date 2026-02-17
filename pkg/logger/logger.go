// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package logger provides a logging capability for toolhive for running locally as a CLI and in Kubernetes.
//
// This is a thin shim over toolhive-core/logging that maintains backward
// compatibility with existing call sites. New code should inject *slog.Logger
// directly; use [Get] to obtain the underlying logger for injection.
package logger

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive-core/logging"
)

// singleton is the package-level logger created by Initialize.
// Accessed atomically to be safe for concurrent use across goroutines.
var singleton atomic.Pointer[slog.Logger]

func init() {
	// Set a default logger so callers that skip Initialize() don't panic.
	singleton.Store(logging.New())
}

// get returns the current singleton logger.
func get() *slog.Logger {
	return singleton.Load()
}

// Get returns the underlying *slog.Logger for injection into structs.
func Get() *slog.Logger {
	return get()
}

// Set replaces the singleton logger. This is intended for tests that need to
// capture log output; production code should use [Initialize] instead.
func Set(l *slog.Logger) {
	singleton.Store(l)
}

// Debug logs a message at debug level using the singleton logger.
func Debug(msg string) {
	get().Debug(msg)
}

// Debugf logs a message at debug level using the singleton logger.
func Debugf(msg string, args ...any) {
	get().Debug(fmt.Sprintf(msg, args...))
}

// Debugw logs a message at debug level using the singleton logger with additional key-value pairs.
func Debugw(msg string, keysAndValues ...any) {
	get().Debug(msg, keysAndValues...)
}

// Info logs a message at info level using the singleton logger.
func Info(msg string) {
	get().Info(msg)
}

// Infof logs a message at info level using the singleton logger.
func Infof(msg string, args ...any) {
	get().Info(fmt.Sprintf(msg, args...))
}

// Infow logs a message at info level using the singleton logger with additional key-value pairs.
func Infow(msg string, keysAndValues ...any) {
	get().Info(msg, keysAndValues...)
}

// Warn logs a message at warning level using the singleton logger.
func Warn(msg string) {
	get().Warn(msg)
}

// Warnf logs a message at warning level using the singleton logger.
func Warnf(msg string, args ...any) {
	get().Warn(fmt.Sprintf(msg, args...))
}

// Warnw logs a message at warning level using the singleton logger with additional key-value pairs.
func Warnw(msg string, keysAndValues ...any) {
	get().Warn(msg, keysAndValues...)
}

// Error logs a message at error level using the singleton logger.
func Error(msg string) {
	get().Error(msg)
}

// Errorf logs a message at error level using the singleton logger.
func Errorf(msg string, args ...any) {
	get().Error(fmt.Sprintf(msg, args...))
}

// Errorw logs a message at error level using the singleton logger with additional key-value pairs.
func Errorw(msg string, keysAndValues ...any) {
	get().Error(msg, keysAndValues...)
}

// Panic logs a message at error level using the singleton logger and panics the program.
func Panic(msg string) {
	get().Error(msg)
	panic(msg)
}

// Panicf logs a message at error level using the singleton logger and panics the program.
func Panicf(msg string, args ...any) {
	formatted := fmt.Sprintf(msg, args...)
	get().Error(formatted)
	panic(formatted)
}

// Panicw logs a message at error level using the singleton logger with additional key-value pairs and panics the program.
func Panicw(msg string, keysAndValues ...any) {
	get().Error(msg, keysAndValues...)
	panic(msg)
}

// DPanic logs a message at error level using the singleton logger.
// Unlike zap's DPanic, this always logs at error level and never panics,
// since slog has no equivalent of development-only panic behavior.
func DPanic(msg string) {
	get().Error(msg)
}

// DPanicf logs a message at error level using the singleton logger.
func DPanicf(msg string, args ...any) {
	get().Error(fmt.Sprintf(msg, args...))
}

// DPanicw logs a message at error level using the singleton logger with additional key-value pairs.
func DPanicw(msg string, keysAndValues ...any) {
	get().Error(msg, keysAndValues...)
}

// Fatal logs a message at error level using the singleton logger and exits the program.
func Fatal(msg string) {
	get().Error(msg)
	os.Exit(1)
}

// Fatalf logs a message at error level using the singleton logger and exits the program.
func Fatalf(msg string, args ...any) {
	get().Error(fmt.Sprintf(msg, args...))
	os.Exit(1)
}

// Fatalw logs a message at error level using the singleton logger with additional key-value pairs and exits the program.
func Fatalw(msg string, keysAndValues ...any) {
	get().Error(msg, keysAndValues...)
	os.Exit(1)
}

// NewLogr returns a logr.Logger backed by the slog singleton.
func NewLogr() logr.Logger {
	return logr.FromSlogHandler(get().Handler())
}

// Initialize creates and configures the appropriate logger.
// If the UNSTRUCTURED_LOGS env var is set to true, it will output plain text.
// Otherwise it will create a standard structured JSON logger.
func Initialize() {
	InitializeWithEnv(&env.OSReader{})
}

// InitializeWithEnv creates and configures the appropriate logger with a custom environment reader.
// This allows for dependency injection of environment variable access for testing.
func InitializeWithEnv(envReader env.Reader) {
	var opts []logging.Option

	if unstructuredLogsWithEnv(envReader) {
		opts = append(opts, logging.WithFormat(logging.FormatText))
	}

	if viper.GetBool("debug") {
		opts = append(opts, logging.WithLevel(slog.LevelDebug))
	}

	singleton.Store(logging.New(opts...))
}

func unstructuredLogsWithEnv(envReader env.Reader) bool {
	unstructuredLogs, err := strconv.ParseBool(envReader.Getenv("UNSTRUCTURED_LOGS"))
	if err != nil {
		// at this point if the error is not nil, the env var wasn't set, or is ""
		// which means we just default to outputting unstructured logs.
		return true
	}
	return unstructuredLogs
}
