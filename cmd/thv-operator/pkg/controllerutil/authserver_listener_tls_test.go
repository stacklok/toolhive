// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
)

func TestGenerateAuthServerVolumesTLSListener(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		listener *mcpv1beta1.TLSListenerConfig
		want     []corev1.VolumeProjection
	}{
		{
			name: "same secret",
			listener: &mcpv1beta1.TLSListenerConfig{
				CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "certificate"},
				PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "private-key"},
			},
			want: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{
				Name: "listener-tls",
				Items: []corev1.KeyToPath{
					{Key: "certificate", Path: AuthServerTLSCertFileName},
					{Key: "private-key", Path: AuthServerTLSKeyFileName},
				},
			}}},
		},
		{
			name: "same secret and same key",
			listener: &mcpv1beta1.TLSListenerConfig{
				CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "bundle"},
				PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "bundle"},
			},
			want: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{
				Name: "listener-tls",
				Items: []corev1.KeyToPath{
					{Key: "bundle", Path: AuthServerTLSCertFileName},
					{Key: "bundle", Path: AuthServerTLSKeyFileName},
				},
			}}},
		},
		{
			name: "different secrets",
			listener: &mcpv1beta1.TLSListenerConfig{
				CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "certificate", Key: "tls.crt"},
				PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "private-key", Key: "tls.key"},
			},
			want: []corev1.VolumeProjection{
				{Secret: &corev1.SecretProjection{Name: "certificate", Items: []corev1.KeyToPath{{Key: "tls.crt", Path: AuthServerTLSCertFileName}}}},
				{Secret: &corev1.SecretProjection{Name: "private-key", Items: []corev1.KeyToPath{{Key: "tls.key", Path: AuthServerTLSKeyFileName}}}},
			},
		},
		{name: "nil listener"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			volumes, mounts, err := GenerateAuthServerVolumes(&mcpv1beta1.EmbeddedAuthServerConfig{TLSListener: test.listener})
			require.NoError(t, err)
			if test.listener == nil {
				assert.Empty(t, volumes)
				assert.Empty(t, mounts)
				return
			}

			require.Len(t, volumes, 1)
			assert.Equal(t, AuthServerTLSVolumeName, volumes[0].Name)
			require.NotNil(t, volumes[0].Projected)
			assert.Equal(t, test.want, volumes[0].Projected.Sources)
			require.NotNil(t, volumes[0].Projected.DefaultMode)
			assert.Equal(t, int32(0400), *volumes[0].Projected.DefaultMode)
			require.Equal(t, []corev1.VolumeMount{{
				Name: AuthServerTLSVolumeName, MountPath: AuthServerTLSMountPath, ReadOnly: true,
			}}, mounts)
		})
	}
}

func TestBuildAuthServerRunConfigTLSListener(t *testing.T) {
	t.Parallel()

	listener := &mcpv1beta1.TLSListenerConfig{
		CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "certificate"},
		PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "private-key"},
	}
	trustDomains := []mcpv1beta1.SPIFFETrustDomainConfig{{
		Name: "example", TrustDomain: "example.org",
		Methods: []mcpv1beta1.SPIFFEAuthenticationMethod{
			mcpv1beta1.SPIFFEAuthenticationMethodX509, mcpv1beta1.SPIFFEAuthenticationMethodJWT,
		},
		BundleSource: mcpv1beta1.SPIFFEBundleSourceConfig{
			Type: mcpv1beta1.SPIFFEBundleSourceTypeFile,
			File: &mcpv1beta1.SPIFFEFileBundleSourceConfig{ConfigMapName: "spire-bundle", ConfigMapKey: "bundle.json"},
		},
	}}
	grants := func(method mcpv1beta1.SPIFFEAuthenticationMethod) *mcpv1beta1.InboundGrantsConfig {
		return &mcpv1beta1.InboundGrantsConfig{SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientConfig{{
			TrustDomainRef:   "example",
			PrincipalPattern: "spiffe://example.org/ns/default/agent",
			ClientID:         "spiffe-client",
			Methods:          []mcpv1beta1.SPIFFEAuthenticationMethod{method},
			Audiences:        []string{"https://mcp.example.com"},
			Scopes:           []string{"openid"},
		}}}
	}

	tests := []struct {
		name         string
		config       *mcpv1beta1.EmbeddedAuthServerConfig
		wantListener bool
		wantErr      string
	}{
		{name: "listener configured", config: &mcpv1beta1.EmbeddedAuthServerConfig{TLSListener: listener}, wantListener: true},
		{name: "no listener", config: &mcpv1beta1.EmbeddedAuthServerConfig{}},
		{
			name: "x509 association without listener",
			config: &mcpv1beta1.EmbeddedAuthServerConfig{
				InboundGrants: &mcpv1beta1.InboundGrantsConfig{SPIFFEClientAuth: []mcpv1beta1.SPIFFEClientConfig{{
					Methods: []mcpv1beta1.SPIFFEAuthenticationMethod{mcpv1beta1.SPIFFEAuthenticationMethodX509},
				}}},
			},
			wantErr: "tlsListener is required when SPIFFE X.509 client authentication is configured",
		},
		{
			name: "x509 association with listener passes the listener rule",
			config: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com", TLSListener: listener, SPIFFETrustDomains: trustDomains,
				InboundGrants: grants(mcpv1beta1.SPIFFEAuthenticationMethodX509),
			},
			wantListener: true,
		},
		{
			name: "jwt-only association without listener passes the listener rule",
			config: &mcpv1beta1.EmbeddedAuthServerConfig{
				SPIFFETrustDomains: trustDomains,
				InboundGrants:      grants(mcpv1beta1.SPIFFEAuthenticationMethodJWT),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config, err := BuildAuthServerRunConfig("default", "server", tt.config,
				[]string{"https://mcp.example.com"}, []string{"openid"}, "https://mcp.example.com")
			if tt.wantErr != "" {
				require.Nil(t, config)
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			if !tt.wantListener {
				assert.Nil(t, config.TLSListener)
				return
			}
			require.NotNil(t, config.TLSListener)
			assert.Equal(t, "/etc/toolhive/authserver/tls/tls.crt", config.TLSListener.CertFile)
			assert.Equal(t, "/etc/toolhive/authserver/tls/tls.key", config.TLSListener.KeyFile)
		})
	}
}

func TestAuthServerVolumesNeedUpdate(t *testing.T) {
	t.Parallel()

	type option func(*mcpv1beta1.EmbeddedAuthServerConfig)
	volumes := func(options ...option) []corev1.Volume {
		config := &mcpv1beta1.EmbeddedAuthServerConfig{}
		for _, apply := range options {
			apply(config)
		}
		generated, _, err := GenerateAuthServerVolumes(config)
		require.NoError(t, err)
		return generated
	}
	listener := func(certSecret, keySecret string) option {
		return func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.TLSListener = &mcpv1beta1.TLSListenerConfig{
				CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: certSecret, Key: "certificate"},
				PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: keySecret, Key: "private-key"},
			}
		}
	}
	signingKey := func(secret string) option {
		return func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.SigningKeySecretRefs = []mcpv1beta1.SecretKeyRef{{Name: secret, Key: "key"}}
		}
	}
	hmacSecret := func(secret string) option {
		return func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.HMACSecretRefs = []mcpv1beta1.SecretKeyRef{{Name: secret, Key: "secret"}}
		}
	}
	bundle := func(configMap string) option {
		return func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.SPIFFETrustDomains = []mcpv1beta1.SPIFFETrustDomainConfig{{
				Name: "example", TrustDomain: "example.org",
				BundleSource: mcpv1beta1.SPIFFEBundleSourceConfig{
					Type: mcpv1beta1.SPIFFEBundleSourceTypeFile,
					File: &mcpv1beta1.SPIFFEFileBundleSourceConfig{ConfigMapName: configMap, ConfigMapKey: "bundle.json"},
				},
			}}
		}
	}
	redisCA := func(masterSecret, sentinelSecret string) option {
		return func(c *mcpv1beta1.EmbeddedAuthServerConfig) {
			c.Storage = &mcpv1beta1.AuthServerStorageConfig{Redis: &mcpv1beta1.RedisStorageConfig{
				TLS:         &mcpv1beta1.RedisTLSConfig{CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: masterSecret, Key: "ca.crt"}},
				SentinelTLS: &mcpv1beta1.RedisTLSConfig{CACertSecretRef: &mcpv1beta1.SecretKeyRef{Name: sentinelSecret, Key: "ca.crt"}},
			}}
		}
	}
	sessionVolume := func(secret string) []corev1.Volume {
		return []corev1.Volume{{
			Name:         SessionRedisTLSCACertVolumeName,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret}},
		}}
	}
	shared := volumes(listener("listener-tls", "listener-tls"))
	separate := volumes(listener("listener-cert", "listener-key"))

	tests := []struct {
		name    string
		live    []corev1.Volume
		desired []corev1.Volume
		want    bool
	}{
		{name: "identical", live: shared, desired: volumes(listener("listener-tls", "listener-tls"))},
		{name: "neither has the volume"},
		{name: "listener added", desired: shared, want: true},
		{name: "listener removed", live: shared, want: true},
		{name: "shared secret split into two secrets", live: shared, desired: separate, want: true},
		{name: "two secrets merged into one shared secret", live: separate, desired: shared, want: true},
		{name: "separate secrets identical", live: separate, desired: volumes(listener("listener-cert", "listener-key"))},
		{name: "unrelated volumes are ignored",
			live:    append([]corev1.Volume{{Name: "other"}}, shared...),
			desired: append([]corev1.Volume{{Name: "different"}}, shared...)},
		{name: "TLS listener secret renamed", live: shared, desired: volumes(listener("other-tls", "other-tls")), want: true},
		{name: "signing key secret renamed", live: volumes(signingKey("a")), desired: volumes(signingKey("b")), want: true},
		{name: "signing key volume removed", live: volumes(signingKey("a")), want: true},
		{name: "HMAC secret renamed", live: volumes(hmacSecret("a")), desired: volumes(hmacSecret("b")), want: true},
		{name: "SPIFFE bundle configmap renamed", live: volumes(bundle("a")), desired: volumes(bundle("b")), want: true},
		{name: "redis master CA secret renamed",
			live: volumes(redisCA("redis-ca", "sentinel-ca")), desired: volumes(redisCA("other-ca", "sentinel-ca")), want: true},
		{name: "redis sentinel CA secret renamed",
			live: volumes(redisCA("redis-ca", "sentinel-ca")), desired: volumes(redisCA("redis-ca", "other-ca")), want: true},
		{name: "session redis CA volume is left to SessionRedisTLSVolumeNeedsUpdate",
			live: sessionVolume("a"), desired: sessionVolume("b")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, AuthServerVolumesNeedUpdate(tt.live, tt.desired))
		})
	}
}

func TestValidateEmbeddedAuthServerTLSListenerPort(t *testing.T) {
	t.Parallel()

	embedded := func(listener *mcpv1beta1.TLSListenerConfig) *mcpv1beta1.MCPExternalAuthConfig {
		return &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type:               mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{TLSListener: listener},
			},
		}
	}
	listener := &mcpv1beta1.TLSListenerConfig{
		CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "certificate"},
		PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "listener-tls", Key: "private-key"},
	}
	extRef := &mcpv1beta1.ExternalAuthConfigRef{Name: "auth"}
	serverRef := &mcpv1beta1.AuthServerRef{Kind: "MCPExternalAuthConfig", Name: "auth"}
	headerInjection := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
		Spec:       mcpv1beta1.MCPExternalAuthConfigSpec{Type: mcpv1beta1.ExternalAuthTypeHeaderInjection},
	}

	tests := []struct {
		name       string
		objects    []client.Object
		extRef     *mcpv1beta1.ExternalAuthConfigRef
		serverRef  *mcpv1beta1.AuthServerRef
		port       int32
		wantSource EmbeddedAuthServerConfigSource
	}{
		{name: "clash via externalAuthConfigRef", objects: []client.Object{embedded(listener)}, extRef: extRef, port: 8443,
			wantSource: EmbeddedAuthServerConfigSourceExternalAuthConfigRef},
		{name: "clash via authServerRef", objects: []client.Object{embedded(listener)}, serverRef: serverRef, port: 8443,
			wantSource: EmbeddedAuthServerConfigSourceAuthServerRef},
		{name: "different port", objects: []client.Object{embedded(listener)}, extRef: extRef, port: 8080},
		{name: "no listener on port 8443", objects: []client.Object{embedded(nil)}, extRef: extRef, port: 8443},
		{name: "non-embedded config on port 8443", objects: []client.Object{headerInjection}, extRef: extRef, port: 8443},
		{name: "missing config on port 8443", extRef: extRef, port: 8443},
		{name: "no reference on port 8443", port: 8443},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := fake.NewClientBuilder().WithScheme(testutil.NewScheme(t)).WithObjects(tt.objects...).Build()
			err := ValidateEmbeddedAuthServerTLSListenerPort(t.Context(), c, "default", tt.extRef, tt.serverRef, tt.port)
			if tt.wantSource == "" {
				require.NoError(t, err)
				return
			}
			var invalidErr *InvalidEmbeddedAuthServerConfigError
			require.ErrorAs(t, err, &invalidErr)
			assert.Equal(t, tt.wantSource, invalidErr.Source)
			assert.ErrorContains(t, err, "proxyPort 8443")
		})
	}
}
