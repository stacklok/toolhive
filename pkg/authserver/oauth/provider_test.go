package oauth

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOAuth2ConfigFromAuthServerConfig(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	config := &AuthServerConfig{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecret:           []byte("test-secret-with-32-bytes-long!!"),
		SigningKeyID:         "key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
	}

	oauth2Config, err := NewOAuth2ConfigFromAuthServerConfig(config)
	require.NoError(t, err)
	require.NotNil(t, oauth2Config)

	// Verify fosite config is set correctly
	assert.Equal(t, config.Issuer, oauth2Config.AccessTokenIssuer)
	assert.Equal(t, config.AccessTokenLifespan, oauth2Config.AccessTokenLifespan)
	assert.Equal(t, config.RefreshTokenLifespan, oauth2Config.RefreshTokenLifespan)
	assert.Equal(t, config.AuthCodeLifespan, oauth2Config.AuthorizeCodeLifespan)

	// Verify signing key is set
	require.NotNil(t, oauth2Config.SigningKey)
	assert.Equal(t, "key-1", oauth2Config.SigningKey.KeyID)
	assert.Equal(t, "RS256", oauth2Config.SigningKey.Algorithm)

	// Verify JWKS contains the key
	require.NotNil(t, oauth2Config.SigningJWKS)
	assert.Len(t, oauth2Config.SigningJWKS.Keys, 1)
}

func TestNewOAuth2ConfigFromAuthServerConfig_InvalidConfig(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tests := []struct {
		name    string
		config  *AuthServerConfig
		wantErr string
	}{
		{
			name: "missing key ID",
			config: &AuthServerConfig{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecret:           []byte("test-secret-with-32-bytes-long!!"),
				SigningKeyID:         "",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "signing key ID is required",
		},
		{
			name: "missing algorithm",
			config: &AuthServerConfig{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecret:           []byte("test-secret-with-32-bytes-long!!"),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "",
				SigningKey:           rsaKey,
			},
			wantErr: "signing key algorithm is required",
		},
		{
			name: "missing signing key",
			config: &AuthServerConfig{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecret:           []byte("test-secret-with-32-bytes-long!!"),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           nil,
			},
			wantErr: "signing key is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewOAuth2ConfigFromAuthServerConfig(tt.config)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestOAuth2Config_PublicJWKS(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	config := &AuthServerConfig{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecret:           []byte("test-secret-with-32-bytes-long!!"),
		SigningKeyID:         "key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
	}

	oauth2Config, err := NewOAuth2ConfigFromAuthServerConfig(config)
	require.NoError(t, err)

	publicJWKS := oauth2Config.PublicJWKS()
	require.NotNil(t, publicJWKS)
	require.Len(t, publicJWKS.Keys, 1)

	// Verify it's a public key (not private)
	_, ok := publicJWKS.Keys[0].Key.(*rsa.PublicKey)
	assert.True(t, ok, "expected public key, got %T", publicJWKS.Keys[0].Key)
}
