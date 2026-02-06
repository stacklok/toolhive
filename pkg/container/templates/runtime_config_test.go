// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package templates

import (
	"strings"
	"testing"
)

func TestGetDefaultRuntimeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		transportType TransportType
		wantImage     string
		wantPackages  []string
	}{
		{
			name:          "Go default config",
			transportType: TransportTypeGO,
			wantImage:     "golang:1.25-alpine",
			wantPackages:  []string{"ca-certificates", "git"},
		},
		{
			name:          "NPX default config",
			transportType: TransportTypeNPX,
			wantImage:     "node:22-alpine",
			wantPackages:  []string{"git", "ca-certificates"},
		},
		{
			name:          "UVX default config",
			transportType: TransportTypeUVX,
			wantImage:     "python:3.13-slim",
			wantPackages:  []string{"ca-certificates", "git"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := GetDefaultRuntimeConfig(tt.transportType)

			if got.BuilderImage != tt.wantImage {
				t.Errorf("BuilderImage = %v, want %v", got.BuilderImage, tt.wantImage)
			}

			if len(got.AdditionalPackages) != len(tt.wantPackages) {
				t.Errorf("AdditionalPackages length = %v, want %v", len(got.AdditionalPackages), len(tt.wantPackages))
			}

			for i, pkg := range tt.wantPackages {
				if got.AdditionalPackages[i] != pkg {
					t.Errorf("AdditionalPackages[%d] = %v, want %v", i, got.AdditionalPackages[i], pkg)
				}
			}
		})
	}
}

func TestGetDockerfileTemplateWithCustomRuntimeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		transportType TransportType
		runtimeConfig *RuntimeConfig
		wantInContent string
	}{
		{
			name:          "Custom Go version",
			transportType: TransportTypeGO,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "golang:1.24-alpine",
				AdditionalPackages: []string{"ca-certificates", "git", "gcc"},
			},
			wantInContent: "FROM golang:1.24-alpine AS builder",
		},
		{
			name:          "Custom Node version",
			transportType: TransportTypeNPX,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "node:20-alpine",
				AdditionalPackages: []string{"git"},
			},
			wantInContent: "FROM node:20-alpine AS builder",
		},
		{
			name:          "Custom Python version",
			transportType: TransportTypeUVX,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "python:3.11-slim",
				AdditionalPackages: []string{"ca-certificates"},
			},
			wantInContent: "FROM python:3.11-slim AS builder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := TemplateData{
				MCPPackage:    "test-package",
				RuntimeConfig: tt.runtimeConfig,
			}

			result, err := GetDockerfileTemplate(tt.transportType, data)
			if err != nil {
				t.Fatalf("GetDockerfileTemplate() error = %v", err)
			}

			if !strings.Contains(result, tt.wantInContent) {
				t.Errorf("Dockerfile does not contain expected content %q", tt.wantInContent)
			}
		})
	}
}

func TestGetDockerfileTemplateUsesDefaultWhenNil(t *testing.T) {
	t.Parallel()

	data := TemplateData{
		MCPPackage:    "test-package",
		RuntimeConfig: nil, // Should use defaults
	}

	result, err := GetDockerfileTemplate(TransportTypeGO, data)
	if err != nil {
		t.Fatalf("GetDockerfileTemplate() error = %v", err)
	}

	// Should use default Go version
	if !strings.Contains(result, "FROM golang:1.25-alpine AS builder") {
		t.Error("Dockerfile does not contain default Go version")
	}
}
