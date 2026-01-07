package logger

import (
	"github.com/go-logr/logr"
)

// logrAdapter adapts ToolHive's logger to the logr.LogSink interface
// required by controller-runtime. This enables controller-runtime components
// (reconcilers, informers, etc.) to use ToolHive's centralized logger.
type logrAdapter struct {
	name   string
	values []interface{}
}

// NewLogrAdapter creates a logr.LogSink that bridges to ToolHive's logger.
// This is used by controller-runtime for reconciler and informer logging.
func NewLogrAdapter() logr.LogSink {
	return &logrAdapter{}
}

// Init implements logr.LogSink.
func (*logrAdapter) Init(_ logr.RuntimeInfo) {
	// No initialization needed
}

// Enabled implements logr.LogSink.
// Returns true if the given log level should be logged.
func (*logrAdapter) Enabled(_ int) bool {
	// Level 0 = Info, Level 1+ = Debug/Verbose
	// Always enable info logs, and enable debug if level > 0
	return true
}

// Info implements logr.LogSink.
// Logs an informational message.
func (l *logrAdapter) Info(level int, msg string, keysAndValues ...interface{}) {
	allValues := append(l.values, keysAndValues...)
	if level == 0 {
		Infow(msg, allValues...)
	} else {
		Debugw(msg, allValues...)
	}
}

// Error implements logr.LogSink.
// Logs an error message.
func (l *logrAdapter) Error(err error, msg string, keysAndValues ...interface{}) {
	allValues := append(l.values, append([]interface{}{"error", err}, keysAndValues...)...)
	Errorw(msg, allValues...)
}

// WithValues implements logr.LogSink.
// Returns a new LogSink with additional key-value pairs.
func (l *logrAdapter) WithValues(keysAndValues ...interface{}) logr.LogSink {
	return &logrAdapter{
		name:   l.name,
		values: append(l.values, keysAndValues...),
	}
}

// WithName implements logr.LogSink.
// Returns a new LogSink with a name prefix.
func (l *logrAdapter) WithName(name string) logr.LogSink {
	newName := name
	if l.name != "" {
		newName = l.name + "." + name
	}
	return &logrAdapter{
		name:   newName,
		values: append([]interface{}{"logger", newName}, l.values...),
	}
}
