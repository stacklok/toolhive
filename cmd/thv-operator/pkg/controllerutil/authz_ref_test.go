// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"

	// Blank-imported so the cedarv1 and httpv1 authorizer factories register
	// themselves; BuildFullAuthzConfigJSON / AddAuthzConfigRefOptions resolve
	// the backend via the authorizers registry.
	_ "github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	_ "github.com/stacklok/toolhive/pkg/authz/authorizers/http"
	"github.com/stacklok/toolhive/pkg/runner"
)

// cedarRefConfig is the backend-specific config blob a cedarv1 MCPAuthzConfig holds.
const cedarRefConfig = `{"policies":["permit(principal, action, resource);"],"entities_json":"[]"}`

// httpRefConfig is the backend-specific config blob an httpv1 MCPAuthzConfig holds.
const httpRefConfig = `{"http":{"url":"https://pdp.example.com"},"claim_mapping":"standard"}`

func newAuthzConfig(name, typ, rawConfig string, valid bool) *mcpv1beta1.MCPAuthzConfig {
	cfg := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{
			Type:   typ,
			Config: runtime.RawExtension{Raw: []byte(rawConfig)},
		},
	}
	status := metav1.ConditionFalse
	if valid {
		status = metav1.ConditionTrue
	}
	cfg.Status.Conditions = []metav1.Condition{{
		Type:   mcpv1beta1.ConditionTypeAuthzConfigValid,
		Status: status,
		Reason: "Test",
	}}
	return cfg
}

func TestBuildFullAuthzConfigJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spec     mcpv1beta1.MCPAuthzConfigSpec
		wantErr  bool
		wantType string
		wantKey  string // the backend config key the envelope must carry
	}{
		{
			name:     "cedarv1",
			spec:     mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: runtime.RawExtension{Raw: []byte(cedarRefConfig)}},
			wantType: "cedarv1",
			wantKey:  "cedar",
		},
		{
			name:     "httpv1",
			spec:     mcpv1beta1.MCPAuthzConfigSpec{Type: "httpv1", Config: runtime.RawExtension{Raw: []byte(httpRefConfig)}},
			wantType: "httpv1",
			wantKey:  "pdp",
		},
		{
			name:    "unregistered type",
			spec:    mcpv1beta1.MCPAuthzConfigSpec{Type: "nopev1", Config: runtime.RawExtension{Raw: []byte(`{}`)}},
			wantErr: true,
		},
		{
			name:    "empty config",
			spec:    mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, factory, err := BuildFullAuthzConfigJSON(tt.spec)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, data, "data must be nil on error")
				assert.Nil(t, factory, "factory must be nil on error")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, factory)

			var envelope map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &envelope))
			assert.JSONEq(t, `"`+tt.wantType+`"`, string(envelope["type"]))
			assert.JSONEq(t, `"`+AuthzConfigVersion+`"`, string(envelope["version"]), "envelope version must be AuthzConfigVersion")
			assert.Contains(t, envelope, tt.wantKey, "envelope must carry the backend config under its ConfigKey")
		})
	}
}

func TestGetAuthzConfigForWorkload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testutil.NewScheme(t)

	cfg := newAuthzConfig("authz", "cedarv1", cedarRefConfig, true)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg).Build()

	t.Run("nil ref returns nil", func(t *testing.T) {
		t.Parallel()
		got, err := GetAuthzConfigForWorkload(ctx, c, "default", nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})
	t.Run("found", func(t *testing.T) {
		t.Parallel()
		got, err := GetAuthzConfigForWorkload(ctx, c, "default", &mcpv1beta1.MCPAuthzConfigReference{Name: "authz"})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "authz", got.Name)
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, err := GetAuthzConfigForWorkload(ctx, c, "default", &mcpv1beta1.MCPAuthzConfigReference{Name: "missing"})
		assert.Error(t, err)
	})
}

func TestValidateAuthzConfigReady(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     *mcpv1beta1.MCPAuthzConfig
		wantErr bool
	}{
		{name: "valid true", cfg: newAuthzConfig("a", "cedarv1", cedarRefConfig, true)},
		{name: "valid false", cfg: newAuthzConfig("a", "cedarv1", cedarRefConfig, false), wantErr: true},
		{
			name:    "condition absent",
			cfg:     &mcpv1beta1.MCPAuthzConfig{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "default"}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAuthzConfigReady(tt.cfg)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestAddAuthzConfigRefOptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := testutil.NewScheme(t)

	cedarCfg := newAuthzConfig("cedar-authz", "cedarv1", cedarRefConfig, true)
	httpCfg := newAuthzConfig("http-authz", "httpv1", httpRefConfig, true)
	notReady := newAuthzConfig("notready-authz", "cedarv1", cedarRefConfig, false)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cedarCfg, httpCfg, notReady).Build()

	t.Run("nil ref is a no-op", func(t *testing.T) {
		t.Parallel()
		var opts []runner.RunConfigBuilderOption
		require.NoError(t, AddAuthzConfigRefOptions(ctx, c, "default", nil, &opts))
		assert.Empty(t, opts)
	})
	t.Run("cedarv1 ref appends one option", func(t *testing.T) {
		t.Parallel()
		var opts []runner.RunConfigBuilderOption
		require.NoError(t, AddAuthzConfigRefOptions(ctx, c, "default", &mcpv1beta1.MCPAuthzConfigReference{Name: "cedar-authz"}, &opts))
		assert.Len(t, opts, 1)
	})
	t.Run("httpv1 ref appends one option (backend-agnostic)", func(t *testing.T) {
		t.Parallel()
		var opts []runner.RunConfigBuilderOption
		require.NoError(t, AddAuthzConfigRefOptions(ctx, c, "default", &mcpv1beta1.MCPAuthzConfigReference{Name: "http-authz"}, &opts))
		assert.Len(t, opts, 1)
	})
	t.Run("not found returns error", func(t *testing.T) {
		t.Parallel()
		var opts []runner.RunConfigBuilderOption
		assert.Error(t, AddAuthzConfigRefOptions(ctx, c, "default", &mcpv1beta1.MCPAuthzConfigReference{Name: "missing"}, &opts))
	})
	t.Run("not ready returns error", func(t *testing.T) {
		t.Parallel()
		var opts []runner.RunConfigBuilderOption
		assert.Error(t, AddAuthzConfigRefOptions(ctx, c, "default", &mcpv1beta1.MCPAuthzConfigReference{Name: "notready-authz"}, &opts))
	})
}
