// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
)

// TestAuthorizationServerParamsGateSPIFFEJWTOnTransport pins that newServer's
// parameter mapping marks a JWT-SVID association as a confidential client, so
// the provider's own transport check still refuses a plain-HTTP non-loopback
// issuer if Config.Validate is bypassed.
func TestAuthorizationServerParamsGateSPIFFEJWTOnTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		trust   *SPIFFETrustConfig
		wantErr string
	}{
		{name: "JWT-SVID association is refused", trust: mustNewSPIFFEJWTTrustConfig(t), wantErr: "plain-HTTP non-loopback"},
		{name: "X.509-only association is unaffected", trust: mustNewSPIFFETrustConfig(t)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)
			cfg := Config{
				Issuer:               "http://auth.example.com",
				KeyProvider:          &testKeyProvider{key: key},
				HMACSecrets:          servercrypto.NewHMACSecrets(make([]byte, 32)),
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: 24 * time.Hour,
				AuthCodeLifespan:     time.Minute,
				AllowedAudiences:     []string{"https://mcp.example.com"},
				SPIFFETrust:          tt.trust,
			}
			signingKey, publicKeys, err := loadKeyMaterial(context.Background(), cfg.KeyProvider)
			require.NoError(t, err)

			_, err = oauthserver.NewAuthorizationServerConfig(
				authorizationServerParams(cfg, signingKey, publicKeys, nil, nil))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
