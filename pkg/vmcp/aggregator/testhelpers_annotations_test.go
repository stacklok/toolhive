// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"github.com/stacklok/toolhive/pkg/vmcp"
)

func boolPtr(b bool) *bool { return &b }

func newTestToolWithAnnotations(name, backendID string, annotations *vmcp.ToolAnnotations) vmcp.Tool {
	return vmcp.Tool{
		Name:        name,
		Description: name + " description",
		InputSchema: map[string]any{"type": "object"},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"result": map[string]any{"type": "string"},
			},
		},
		Annotations: annotations,
		BackendID:   backendID,
	}
}
