// Package logger provides a logging capability for toolhive for running locally as a CLI and in Kubernetes
package logger

import (
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Debug logs a message at debug level using the singleton logger.
func Debug(msg string) {
	zap.S().Debug(msg)
}

// Debugf logs a message at debug level using the singleton logger.
func Debugf(msg string, args ...any) {
	zap.S().Debugf(msg, args...)
}

// Debugw logs a message at debug level using the singleton logger with additional key-value pairs.
func Debugw(msg string, keysAndValues ...any) {
	zap.S().Debugw(msg, keysAndValues...)
}

// Info logs a message at info level using the singleton logger.
func Info(msg string) {
	zap.S().Info(msg)
}

// Infof logs a message at info level using the singleton logger.
func Infof(msg string, args ...any) {
	zap.S().Infof(msg, args...)
}

// Infow logs a message at info level using the singleton logger with additional key-value pairs.
func Infow(msg string, keysAndValues ...any) {
	zap.S().Infow(msg, keysAndValues...)
}

// Warn logs a message at warning level using the singleton logger.
func Warn(msg string) {
	zap.S().Warn(msg)
}

// Warnf logs a message at warning level using the singleton logger.
func Warnf(msg string, args ...any) {
	zap.S().Warnf(msg, args...)
}

// Warnw logs a message at warning level using the singleton logger with additional key-value pairs.
func Warnw(msg string, keysAndValues ...any) {
	zap.S().Warnw(msg, keysAndValues...)
}

// Error logs a message at error level using the singleton logger.
func Error(msg string) {
	zap.S().Error(msg)
}

// Errorf logs a message at error level using the singleton logger.
func Errorf(msg string, args ...any) {
	zap.S().Errorf(msg, args...)
}

// Errorw logs a message at error level using the singleton logger with additional key-value pairs.
func Errorw(msg string, keysAndValues ...any) {
	zap.S().Errorw(msg, keysAndValues...)
}

// Panic logs a message at error level using the singleton logger and panics the program.
func Panic(msg string) {
	zap.S().Panic(msg)
}

// Panicf logs a message at error level using the singleton logger and panics the program.
func Panicf(msg string, args ...any) {
	zap.S().Panicf(msg, args...)
}

// Panicw logs a message at error level using the singleton logger with additional key-value pairs and panics the program.
func Panicw(msg string, keysAndValues ...any) {
	zap.S().Panicw(msg, keysAndValues...)
}

// DPanic logs a message at error level using the singleton logger and panics the program.
func DPanic(msg string) {
	zap.S().DPanic(msg)
}

// DPanicf logs a message at error level using the singleton logger and panics the program.
func DPanicf(msg string, args ...any) {
	zap.S().DPanicf(msg, args...)
}

// DPanicw logs a message at error level using the singleton logger with additional key-value pairs and panics the program.
func DPanicw(msg string, keysAndValues ...any) {
	zap.S().DPanicw(msg, keysAndValues...)
}

// Fatal logs a message at error level using the singleton logger and exits the program.
func Fatal(msg string) {
	zap.S().Fatal(msg)
}

// Fatalf logs a message at error level using the singleton logger and exits the program.
func Fatalf(msg string, args ...any) {
	zap.S().Fatalf(msg, args...)
}

// Fatalw logs a message at error level using the singleton logger with additional key-value pairs and exits the program.
func Fatalw(msg string, keysAndValues ...any) {
	zap.S().Fatalw(msg, keysAndValues...)
}

// NewLogr returns a logr.Logger which uses zap logger
func NewLogr() logr.Logger {
	return zapr.NewLogger(zap.L())
}

// Initialize creates and configures the appropriate logger.
// If the UNSTRUCTURED_LOGS is set to true, it will output plain log message
// with only time and LogLevelType (INFO, DEBUG, ERROR, WARN)).
// Otherwise it will create a standard structured slog logger
func Initialize() {
	var config zap.Config
	if unstructuredLogs() {
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		config.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(time.Kitchen)
		config.OutputPaths = []string{"stderr"}
		config.DisableStacktrace = true
		config.DisableCaller = true
	} else {
		config = zap.NewProductionConfig()
		config.OutputPaths = []string{"stdout"}
	}

	// Set log level based on current debug flag
	if viper.GetBool("debug") {
		config.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	} else {
		config.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	zap.ReplaceGlobals(zap.Must(config.Build()))
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
