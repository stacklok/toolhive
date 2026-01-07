// Package errors contains error definitions for workloads
// It is located in a separate package to side-step an import cycle
package errors

import (
	"errors"
	"net/http"

	thverrors "github.com/stacklok/toolhive/pkg/errors"
)

// ErrRunConfigNotFound is returned when a run config cannot be found for a workload.
var ErrRunConfigNotFound = thverrors.WithCode(
	errors.New("run config not found"),
	http.StatusNotFound,
)
