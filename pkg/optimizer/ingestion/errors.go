// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ingestion provides services for ingesting MCP tools into the database.
package ingestion

import "errors"

var (
	// ErrIngestionFailed is returned when ingestion fails
	ErrIngestionFailed = errors.New("ingestion failed")

	// ErrBackendRetrievalFailed is returned when backend retrieval fails
	ErrBackendRetrievalFailed = errors.New("backend retrieval failed")

	// ErrToolHiveUnavailable is returned when ToolHive is unavailable
	ErrToolHiveUnavailable = errors.New("ToolHive unavailable")

	// ErrBackendStatusNil is returned when backend status is nil
	ErrBackendStatusNil = errors.New("backend status cannot be nil")

	// ErrInvalidRuntimeMode is returned for invalid runtime mode
	ErrInvalidRuntimeMode = errors.New("invalid runtime mode: must be 'docker' or 'k8s'")
)
