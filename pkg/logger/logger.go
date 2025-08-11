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

// NewLogger creates a new zap sugared logger instance
func NewLogger() *zap.SugaredLogger {
	config := buildConfig()
	logger, err := config.Build()

	if err != nil {
		panic(err) // TODO: handle error appropriately
	}

	return logger.Sugar()
}

// NewLogr returns a logr.Logger which uses zap logger, name to be updated once NewLogr is removed
func NewLogr() logr.Logger {
	sugaredLogger := NewLogger()
	logger := sugaredLogger.Desugar()

	return zapr.NewLogger(logger)
}

// TODO: Update the config as per the project's requirements
// buildConfig returns the cached base configuration
func buildConfig() zap.Config {
	var config zap.Config
	if unstructuredLogs() {
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		config.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(time.Kitchen)
		config.OutputPaths = []string{"stderr"}
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

	return config
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
