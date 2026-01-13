// Package errors contains error definitions for workloads
// It is located in a separate package to side-step an import cycle
package errors

import "errors"

// ErrRunConfigNotFound is returned when a run config cannot be found for a workload.
var ErrRunConfigNotFound = errors.New("run config not found")
