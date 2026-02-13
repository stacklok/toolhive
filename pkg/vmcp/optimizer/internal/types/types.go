// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package types defines shared types used across optimizer sub-packages.
package types

// ToolMatch represents a tool that matched the search criteria.
type ToolMatch struct {
	// Name is the unique identifier of the tool.
	Name string `json:"name"`

	// Description is the human-readable description of the tool.
	Description string `json:"description"`

	// Score indicates how well this tool matches the search criteria (0.0-1.0).
	Score float64 `json:"score"`
}
