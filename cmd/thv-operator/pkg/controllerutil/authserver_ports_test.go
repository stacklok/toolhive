// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestResolveTLSListenerEnabled(t *testing.T) {
	t.Parallel()

	listener := &mcpv1beta1.TLSListenerConfig{}
	tests := []struct {
		name string
		ref  *mcpv1beta1.ExternalAuthConfigRef
		cfg  *mcpv1beta1.MCPExternalAuthConfig
		want bool
	}{
		{name: "no references"},
		{name: "listener enabled", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "auth"}, cfg: externalAuthConfigForTLSListener("auth", mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer, listener), want: true},
		{name: "listener disabled", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "auth"}, cfg: externalAuthConfigForTLSListener("auth", mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer, nil)},
		{name: "non embedded config", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "auth"}, cfg: externalAuthConfigForTLSListener("auth", mcpv1beta1.ExternalAuthTypeHeaderInjection, listener)},
		{name: "missing config", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "missing"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))
			objects := []runtime.Object{}
			if test.cfg != nil {
				objects = append(objects, test.cfg)
			}
			client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
			got, err := ResolveTLSListenerEnabled(context.Background(), client, "default", test.ref, nil)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func externalAuthConfigForTLSListener(name string, authType mcpv1beta1.ExternalAuthType, listener *mcpv1beta1.TLSListenerConfig) *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type:               authType,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{TLSListener: listener},
		},
	}
}
