// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	thvjson "github.com/stacklok/toolhive/pkg/json"
)

func TestValidateForEachStep(t *testing.T) {
	t.Parallel()

	validInnerArgs := thvjson.NewMap(map[string]any{
		"package_name": "{{.forEach.pkg.name}}",
	})

	tests := []struct {
		name    string
		step    WorkflowStepConfig
		wantErr string
	}{
		{
			name: "valid forEach step",
			step: WorkflowStepConfig{
				ID:         "check_vulns",
				Type:       WorkflowStepTypeForEach,
				Collection: "{{json .steps.get_packages.output.packages}}",
				ItemVar:    "pkg",
				InnerStep: &WorkflowStepConfig{
					ID:        "inner",
					Type:      "tool",
					Tool:      "osv.query_vulnerability",
					Arguments: validInnerArgs,
				},
			},
		},
		{
			name: "forEach without collection",
			step: WorkflowStepConfig{
				ID:   "bad",
				Type: WorkflowStepTypeForEach,
				InnerStep: &WorkflowStepConfig{
					ID:   "inner",
					Type: "tool",
					Tool: "osv.query_vulnerability",
				},
			},
			wantErr: "collection is required",
		},
		{
			name: "forEach without inner step",
			step: WorkflowStepConfig{
				ID:         "bad",
				Type:       WorkflowStepTypeForEach,
				Collection: "{{json .steps.get_packages.output.packages}}",
			},
			wantErr: "step is required",
		},
		{
			name: "forEach with tool field set",
			step: WorkflowStepConfig{
				ID:         "bad",
				Type:       WorkflowStepTypeForEach,
				Tool:       "some.tool",
				Collection: "{{json .steps.get_packages.output.packages}}",
				InnerStep: &WorkflowStepConfig{
					ID:   "inner",
					Type: "tool",
					Tool: "osv.query_vulnerability",
				},
			},
			wantErr: "must not have 'tool' field",
		},
		{
			name: "forEach with message field set",
			step: WorkflowStepConfig{
				ID:         "bad",
				Type:       WorkflowStepTypeForEach,
				Message:    "hello",
				Collection: "{{json .steps.get_packages.output.packages}}",
				InnerStep: &WorkflowStepConfig{
					ID:   "inner",
					Type: "tool",
					Tool: "osv.query_vulnerability",
				},
			},
			wantErr: "must not have 'message' field",
		},
		{
			name: "forEach with elicitation inner step",
			step: WorkflowStepConfig{
				ID:         "bad",
				Type:       WorkflowStepTypeForEach,
				Collection: "{{json .steps.get_packages.output.packages}}",
				InnerStep: &WorkflowStepConfig{
					ID:      "inner",
					Type:    "elicitation",
					Message: "hello",
				},
			},
			wantErr: "step.type must be 'tool'",
		},
		{
			name: "forEach with invalid itemVar",
			step: WorkflowStepConfig{
				ID:         "bad",
				Type:       WorkflowStepTypeForEach,
				Collection: "{{json .steps.get_packages.output.packages}}",
				ItemVar:    "123invalid",
				InnerStep: &WorkflowStepConfig{
					ID:   "inner",
					Type: "tool",
					Tool: "osv.query_vulnerability",
				},
			},
			wantErr: "itemVar must be a valid Go identifier",
		},
		{
			name: "forEach with maxIterations exceeding cap",
			step: WorkflowStepConfig{
				ID:            "bad",
				Type:          WorkflowStepTypeForEach,
				Collection:    "{{json .steps.get_packages.output.packages}}",
				MaxIterations: 1001,
				InnerStep: &WorkflowStepConfig{
					ID:   "inner",
					Type: "tool",
					Tool: "osv.query_vulnerability",
				},
			},
			wantErr: "maxIterations must be <= 1000",
		},
		{
			name: "forEach with invalid collection template",
			step: WorkflowStepConfig{
				ID:         "bad",
				Type:       WorkflowStepTypeForEach,
				Collection: "{{.steps.get_packages.output.packages",
				InnerStep: &WorkflowStepConfig{
					ID:   "inner",
					Type: "tool",
					Tool: "osv.query_vulnerability",
				},
			},
			wantErr: "invalid template",
		},
		{
			name: "forEach inner step without tool",
			step: WorkflowStepConfig{
				ID:         "bad",
				Type:       WorkflowStepTypeForEach,
				Collection: "{{json .steps.get_packages.output.packages}}",
				InnerStep: &WorkflowStepConfig{
					ID:   "inner",
					Type: "tool",
				},
			},
			wantErr: "step.tool is required",
		},
		{
			name: "forEach with itemVar set to reserved index",
			step: WorkflowStepConfig{
				ID:         "bad",
				Type:       WorkflowStepTypeForEach,
				Collection: "{{json .steps.get_packages.output.packages}}",
				ItemVar:    "index",
				InnerStep: &WorkflowStepConfig{
					ID:   "inner",
					Type: "tool",
					Tool: "osv.query_vulnerability",
				},
			},
			wantErr: "cannot be 'index'",
		},
		{
			name: "forEach with maxParallel exceeding cap",
			step: WorkflowStepConfig{
				ID:          "bad",
				Type:        WorkflowStepTypeForEach,
				Collection:  "{{json .steps.get_packages.output.packages}}",
				MaxParallel: 51,
				InnerStep: &WorkflowStepConfig{
					ID:   "inner",
					Type: "tool",
					Tool: "osv.query_vulnerability",
				},
			},
			wantErr: "maxParallel must be <= 50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stepIDs := map[string]bool{tt.step.ID: true}
			err := ValidateWorkflowStep("steps", 0, &tt.step, stepIDs)

			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestIsValidGoIdentifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		valid bool
	}{
		{"item", true},
		{"pkg", true},
		{"_foo", true},
		{"foo123", true},
		{"", false},
		{"123abc", false},
		{"foo-bar", false},
		{"foo.bar", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.valid, isValidGoIdentifier(tt.input))
		})
	}
}
