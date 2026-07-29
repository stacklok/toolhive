// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"net/http"
	"testing"

	nameref "github.com/google/go-containerregistry/pkg/name"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	ocimocks "github.com/stacklok/toolhive-core/oci/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/plugins"
	plugmocks "github.com/stacklok/toolhive/pkg/plugins/mocks"
)

func TestValidateOCIRegistryHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ref      string
		wantCode int
		wantErr  string
	}{
		{
			name:     "localhost registry rejected",
			ref:      "localhost:5000/org/plugin:v1",
			wantCode: http.StatusBadRequest,
			wantErr:  "localhost is rejected for SSRF prevention",
		},
		{
			name:     "loopback IP registry rejected",
			ref:      "127.0.0.1:5000/org/plugin:v1",
			wantCode: http.StatusBadRequest,
			wantErr:  "rejected for SSRF prevention",
		},
		{
			name:     "private IP registry rejected",
			ref:      "10.0.0.5/org/plugin:v1",
			wantCode: http.StatusBadRequest,
			wantErr:  "private/loopback IPs are rejected for SSRF prevention",
		},
		{
			name: "public registry allowed",
			ref:  "ghcr.io/org/plugin:v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ref, err := nameref.ParseReference(tt.ref)
			require.NoError(t, err)

			err = validateOCIRegistryHost(ref)
			if tt.wantCode == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, httperr.Code(err))
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// Dev mode relaxes the SSRF check so E2E tests can use a local registry.
// Not parallel: mutates the process environment.
func TestValidateOCIRegistryHost_DevMode(t *testing.T) {
	t.Setenv("TOOLHIVE_DEV", "true")

	ref, err := nameref.ParseReference("localhost:5000/org/plugin:v1")
	require.NoError(t, err)
	assert.NoError(t, validateOCIRegistryHost(ref))
}

func TestInstallOCIRejectsPrivateRegistryHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  string
	}{
		{name: "loopback registry", ref: "127.0.0.1:5000/org/plugin:v1"},
		{name: "localhost registry", ref: "localhost:5000/org/plugin:v1"},
		{name: "private IP registry", ref: "10.0.0.5/org/plugin:v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			// No Pull expectation: gomock fails the test if Pull is called.
			reg := ocimocks.NewMockRegistryClient(ctrl)
			adapter := plugmocks.NewMockMaterializationAdapter(ctrl)
			ociStore, err := ociplugins.NewStore(t.TempDir())
			require.NoError(t, err)

			svc := newTestService(
				WithRegistryClient(reg),
				WithOCIStore(ociStore),
				WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}),
			)
			_, err = svc.Install(t.Context(), plugins.InstallOptions{Name: tt.ref})
			require.Error(t, err)
			assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
			assert.Contains(t, err.Error(), "SSRF prevention")
		})
	}
}

func TestGetContentRejectsPrivateRegistryHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  string
	}{
		{name: "loopback registry", ref: "127.0.0.1:5000/org/plugin:v1"},
		{name: "localhost registry", ref: "localhost:5000/org/plugin:v1"},
		{name: "private IP registry", ref: "10.0.0.5/org/plugin:v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			// No Pull expectation: gomock fails the test if Pull is called.
			reg := ocimocks.NewMockRegistryClient(ctrl)
			ociStore, err := ociplugins.NewStore(t.TempDir())
			require.NoError(t, err)

			svc := New(WithOCIStore(ociStore), WithRegistryClient(reg))
			_, err = svc.GetContent(t.Context(), plugins.ContentOptions{Reference: tt.ref})
			require.Error(t, err)
			assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
			assert.Contains(t, err.Error(), "SSRF prevention")
		})
	}
}
