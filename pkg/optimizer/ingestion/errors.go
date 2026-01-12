// Package ingestion provides services for ingesting MCP tools into the database.
package ingestion

import "errors"

var (
	// ErrIngestionFailed is returned when ingestion fails
	ErrIngestionFailed = errors.New("ingestion failed")

	// ErrWorkloadRetrievalFailed is returned when workload retrieval fails
	ErrWorkloadRetrievalFailed = errors.New("workload retrieval failed")

	// ErrToolHiveUnavailable is returned when ToolHive is unavailable
	ErrToolHiveUnavailable = errors.New("ToolHive unavailable")

	// ErrWorkloadStatusNil is returned when workload status is nil
	ErrWorkloadStatusNil = errors.New("workload status cannot be nil")

	// ErrInvalidRuntimeMode is returned for invalid runtime mode
	ErrInvalidRuntimeMode = errors.New("invalid runtime mode: must be 'docker' or 'k8s'")
)
