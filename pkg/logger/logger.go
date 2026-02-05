// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package logger provides a logging capability for toolhive for running locally as a CLI and in Kubernetes.
// This package re-exports the logger from toolhive-core and provides viper integration.
package logger

import (
	"github.com/go-logr/logr"
	"github.com/spf13/viper"

	corelogger "github.com/stacklok/toolhive-core/logger"
)

// viperDebugProvider implements the DebugProvider interface using viper configuration.
type viperDebugProvider struct{}

// IsDebug returns true if the "debug" viper flag is set.
func (*viperDebugProvider) IsDebug() bool {
	return viper.GetBool("debug")
}

// Debug logs a message at debug level using the singleton logger.
func Debug(msg string) {
	corelogger.Debug(msg)
}

// Debugf logs a message at debug level using the singleton logger.
func Debugf(msg string, args ...any) {
	corelogger.Debugf(msg, args...)
}

// Debugw logs a message at debug level using the singleton logger with additional key-value pairs.
func Debugw(msg string, keysAndValues ...any) {
	corelogger.Debugw(msg, keysAndValues...)
}

// Info logs a message at info level using the singleton logger.
func Info(msg string) {
	corelogger.Info(msg)
}

// Infof logs a message at info level using the singleton logger.
func Infof(msg string, args ...any) {
	corelogger.Infof(msg, args...)
}

// Infow logs a message at info level using the singleton logger with additional key-value pairs.
func Infow(msg string, keysAndValues ...any) {
	corelogger.Infow(msg, keysAndValues...)
}

// Warn logs a message at warning level using the singleton logger.
func Warn(msg string) {
	corelogger.Warn(msg)
}

// Warnf logs a message at warning level using the singleton logger.
func Warnf(msg string, args ...any) {
	corelogger.Warnf(msg, args...)
}

// Warnw logs a message at warning level using the singleton logger with additional key-value pairs.
func Warnw(msg string, keysAndValues ...any) {
	corelogger.Warnw(msg, keysAndValues...)
}

// Error logs a message at error level using the singleton logger.
func Error(msg string) {
	corelogger.Error(msg)
}

// Errorf logs a message at error level using the singleton logger.
func Errorf(msg string, args ...any) {
	corelogger.Errorf(msg, args...)
}

// Errorw logs a message at error level using the singleton logger with additional key-value pairs.
func Errorw(msg string, keysAndValues ...any) {
	corelogger.Errorw(msg, keysAndValues...)
}

// Panic logs a message at error level using the singleton logger and panics the program.
func Panic(msg string) {
	corelogger.Panic(msg)
}

// Panicf logs a message at error level using the singleton logger and panics the program.
func Panicf(msg string, args ...any) {
	corelogger.Panicf(msg, args...)
}

// Panicw logs a message at error level using the singleton logger with additional key-value pairs and panics the program.
func Panicw(msg string, keysAndValues ...any) {
	corelogger.Panicw(msg, keysAndValues...)
}

// DPanic logs a message at error level using the singleton logger and panics the program.
func DPanic(msg string) {
	corelogger.DPanic(msg)
}

// DPanicf logs a message at error level using the singleton logger and panics the program.
func DPanicf(msg string, args ...any) {
	corelogger.DPanicf(msg, args...)
}

// DPanicw logs a message at error level using the singleton logger with additional key-value pairs and panics the program.
func DPanicw(msg string, keysAndValues ...any) {
	corelogger.DPanicw(msg, keysAndValues...)
}

// Fatal logs a message at error level using the singleton logger and exits the program.
func Fatal(msg string) {
	corelogger.Fatal(msg)
}

// Fatalf logs a message at error level using the singleton logger and exits the program.
func Fatalf(msg string, args ...any) {
	corelogger.Fatalf(msg, args...)
}

// Fatalw logs a message at error level using the singleton logger with additional key-value pairs and exits the program.
func Fatalw(msg string, keysAndValues ...any) {
	corelogger.Fatalw(msg, keysAndValues...)
}

// NewLogr returns a logr.Logger which uses zap logger
func NewLogr() logr.Logger {
	return corelogger.NewLogr()
}

// Initialize creates and configures the appropriate logger.
// If the UNSTRUCTURED_LOGS is set to true, it will output plain log message
// with only time and LogLevelType (INFO, DEBUG, ERROR, WARN)).
// Otherwise it will create a standard structured slog logger.
// This version uses viper for the debug flag configuration.
func Initialize() {
	corelogger.InitializeWithDebug(&viperDebugProvider{})
}

// InitializeWithEnv creates and configures the appropriate logger with a custom environment reader.
// This allows for dependency injection of environment variable access for testing.
// Deprecated: Use InitializeWithOptions from toolhive-core/logger instead.
func InitializeWithEnv(envReader interface{ Getenv(key string) string }) {
	// Create a wrapper that implements env.Reader
	corelogger.InitializeWithOptions(&envReaderWrapper{reader: envReader}, &viperDebugProvider{})
}

// envReaderWrapper wraps any type with Getenv method to implement env.Reader
type envReaderWrapper struct {
	reader interface{ Getenv(key string) string }
}

func (w *envReaderWrapper) Getenv(key string) string {
	return w.reader.Getenv(key)
}
