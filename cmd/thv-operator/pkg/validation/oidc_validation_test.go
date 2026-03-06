// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

func TestValidateCABundleSource(t *testing.T) {
	t.Parallel()

	// maxConfigMapNameLength is the max name length that fits in a Kubernetes volume name
	// when prefixed with "oidc-ca-bundle-" (63 - 15 = 48)
	const maxConfigMapNameLength = 48

	tests := []struct {
		name        string
		ref         *mcpv1alpha1.CABundleSource
		wantErr     bool
		errContains string
	}{
		{
			name:    "nil ref is valid",
			ref:     nil,
			wantErr: false,
		},
		{
			name:    "valid configMapRef with name only",
			ref:     makeCABundleSource("my-ca", ""),
			wantErr: false,
		},
		{
			name:    "valid configMapRef with name and key",
			ref:     makeCABundleSource("my-ca", "ca.crt"),
			wantErr: false,
		},
		{
			name:        "missing configMapRef",
			ref:         &mcpv1alpha1.CABundleSource{},
			wantErr:     true,
			errContains: "configMapRef must be specified in caBundleRef",
		},
		{
			name:        "empty configMapRef name",
			ref:         makeCABundleSource("", ""),
			wantErr:     true,
			errContains: "configMapRef.name must be specified",
		},
		{
			name:    "configMapRef name at max length",
			ref:     makeCABundleSource(strings.Repeat("a", maxConfigMapNameLength), ""),
			wantErr: false,
		},
		{
			name:        "configMapRef name too long",
			ref:         makeCABundleSource(strings.Repeat("a", maxConfigMapNameLength+1), ""),
			wantErr:     true,
			errContains: "is too long",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateCABundleSource(tt.ref)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.ErrorContains(t, err, tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateOIDCIssuerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		issuer        string
		allowInsecure bool
		wantErr       bool
		errContains   string
	}{
		{
			name:          "empty issuer is valid",
			issuer:        "",
			allowInsecure: false,
			wantErr:       false,
		},
		{
			name:          "HTTPS issuer is valid",
			issuer:        "https://accounts.example.com",
			allowInsecure: false,
			wantErr:       false,
		},
		{
			name:          "HTTP issuer with allowInsecure true is valid",
			issuer:        "http://dev.example.com",
			allowInsecure: true,
			wantErr:       false,
		},
		{
			name:          "HTTP issuer with allowInsecure false is an error",
			issuer:        "http://dev.example.com",
			allowInsecure: false,
			wantErr:       true,
			errContains:   "HTTP scheme",
		},
		{
			name:          "malformed URL without scheme is an error",
			issuer:        "not-a-url",
			allowInsecure: false,
			wantErr:       true,
			errContains:   "malformed",
		},
		{
			name:          "unsupported scheme is an error",
			issuer:        "ftp://example.com",
			allowInsecure: false,
			wantErr:       true,
			errContains:   "unsupported scheme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validation.ValidateOIDCIssuerURL(tt.issuer, tt.allowInsecure)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// makeCABundleSource creates a CABundleSource with the given name and optional key.
func makeCABundleSource(name, key string) *mcpv1alpha1.CABundleSource {
	return &mcpv1alpha1.CABundleSource{
		ConfigMapRef: &corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: name},
			Key:                  key,
		},
	}
}
