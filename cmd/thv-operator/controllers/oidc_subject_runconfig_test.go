// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/runner"
)

// TestRunConfig_KubernetesServiceAccountSubjectRestriction verifies that the
// serviceAccount/namespace fields of a kubernetesServiceAccount MCPOIDCConfig
// reach the token validator config of both MCPServer and MCPRemoteProxy.
func TestRunConfig_KubernetesServiceAccountSubjectRestriction(t *testing.T) {
	t.Parallel()

	const (
		oidcConfigName = "k8s-sa"
		workloadNS     = "workloads"
	)

	tests := []struct {
		name       string
		ksa        *mcpv1beta1.KubernetesServiceAccountOIDCConfig
		wantSub    string
		wantPrefix string
	}{
		{
			name:    "serviceAccount defaults to workload namespace",
			ksa:     &mcpv1beta1.KubernetesServiceAccountOIDCConfig{ServiceAccount: "agent"},
			wantSub: "system:serviceaccount:workloads:agent",
		},
		{
			name:       "namespace only restricts to subject prefix",
			ksa:        &mcpv1beta1.KubernetesServiceAccountOIDCConfig{Namespace: "clients"},
			wantPrefix: "system:serviceaccount:clients:",
		},
		{
			name: "neither set leaves subject unrestricted",
			ksa:  &mcpv1beta1.KubernetesServiceAccountOIDCConfig{},
		},
	}

	builders := map[string]func(t *testing.T, oidcCfg *mcpv1beta1.MCPOIDCConfig) *runner.RunConfig{
		"MCPServer": func(t *testing.T, oidcCfg *mcpv1beta1.MCPOIDCConfig) *runner.RunConfig {
			t.Helper()
			server := v1beta1test.NewMCPServer("srv", workloadNS,
				v1beta1test.WithImage("test:v1"),
				v1beta1test.WithOIDCConfigRef(oidcConfigName, "my-aud"),
			)
			scheme := testutil.NewScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithRuntimeObjects([]runtime.Object{server, oidcCfg}...).Build()
			cfg, err := newTestMCPServerReconciler(c, scheme, kubernetes.PlatformKubernetes).
				createRunConfigFromMCPServer(server)
			require.NoError(t, err)
			return cfg
		},
		"MCPRemoteProxy": func(t *testing.T, oidcCfg *mcpv1beta1.MCPOIDCConfig) *runner.RunConfig {
			t.Helper()
			proxy := v1beta1test.NewMCPRemoteProxy("proxy", workloadNS,
				v1beta1test.WithRemoteProxyOIDCConfigRef(oidcConfigName, "my-aud"),
			)
			scheme := testutil.NewScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithRuntimeObjects([]runtime.Object{proxy, oidcCfg}...).Build()
			cfg, err := (&MCPRemoteProxyReconciler{Client: c, Scheme: scheme}).
				createRunConfigFromMCPRemoteProxy(t.Context(), proxy)
			require.NoError(t, err)
			return cfg
		},
	}

	for kind, build := range builders {
		for _, tt := range tests {
			t.Run(kind+"/"+tt.name, func(t *testing.T) {
				t.Parallel()

				oidcCfg := &mcpv1beta1.MCPOIDCConfig{
					ObjectMeta: metav1.ObjectMeta{Name: oidcConfigName, Namespace: workloadNS},
					Spec: mcpv1beta1.MCPOIDCConfigSpec{
						Type:                     mcpv1beta1.MCPOIDCConfigTypeKubernetesServiceAccount,
						KubernetesServiceAccount: tt.ksa,
					},
				}

				cfg := build(t, oidcCfg)
				require.NotNil(t, cfg.OIDCConfig)
				assert.Equal(t, tt.wantSub, cfg.OIDCConfig.RequiredSubject)
				assert.Equal(t, tt.wantPrefix, cfg.OIDCConfig.RequiredSubjectPrefix)
			})
		}
	}
}
