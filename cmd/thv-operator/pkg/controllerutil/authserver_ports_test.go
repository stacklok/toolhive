// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestResolveTLSListenerEnabled(t *testing.T) {
	t.Parallel()

	listener := &mcpv1beta1.TLSListenerConfig{}
	tests := []struct {
		name string
		ref  *mcpv1beta1.ExternalAuthConfigRef
		cfg  *mcpv1beta1.MCPExternalAuthConfig
		// getErr makes the Get of the referenced MCPExternalAuthConfig fail.
		getErr  error
		want    bool
		wantErr bool
	}{
		{name: "no references"},
		{name: "listener enabled", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "auth"}, cfg: externalAuthConfigForTLSListener(mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer, listener), want: true},
		{name: "listener disabled", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "auth"}, cfg: externalAuthConfigForTLSListener(mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer, nil)},
		{name: "non embedded config", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "auth"}, cfg: externalAuthConfigForTLSListener(mcpv1beta1.ExternalAuthTypeHeaderInjection, listener)},
		{name: "missing config", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "missing"}},
		{name: "lookup fails", ref: &mcpv1beta1.ExternalAuthConfigRef{Name: "auth"}, cfg: externalAuthConfigForTLSListener(mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer, listener), getErr: errors.New("boom"), wantErr: true},
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
			c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*mcpv1beta1.MCPExternalAuthConfig); ok && test.getErr != nil {
							return test.getErr
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}).Build()
			got, err := ResolveTLSListenerEnabled(context.Background(), c, "default", test.ref, nil)
			if test.wantErr {
				require.ErrorIs(t, err, test.getErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, test.want, got)
		})
	}
}

func externalAuthConfigForTLSListener(authType mcpv1beta1.ExternalAuthType, listener *mcpv1beta1.TLSListenerConfig) *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type:               authType,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{TLSListener: listener},
		},
	}
}
