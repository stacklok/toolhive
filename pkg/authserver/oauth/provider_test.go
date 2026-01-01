package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOAuth2Config(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	config := &Config{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		Secret:               []byte("test-secret-with-32-bytes-long!!"),
		PrivateKeys: []PrivateKey{
			{
				KeyID:     "key-1",
				Algorithm: "RS256",
				Key:       rsaKey,
			},
		},
	}

	oauth2Config, err := NewOAuth2Config(config)
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

	// Verify JWKS contains all keys
	require.NotNil(t, oauth2Config.SigningJWKS)
	assert.Len(t, oauth2Config.SigningJWKS.Keys, 1)
}

func TestNewOAuth2Config_MultipleKeys(t *testing.T) {
	t.Parallel()

	rsaKey1, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rsaKey2, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	config := &Config{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		Secret:               []byte("test-secret-with-32-bytes-long!!"),
		PrivateKeys: []PrivateKey{
			{KeyID: "key-1", Algorithm: "RS256", Key: rsaKey1},
			{KeyID: "key-2", Algorithm: "RS384", Key: rsaKey2},
			{KeyID: "key-3", Algorithm: "ES256", Key: ecdsaKey},
		},
	}

	oauth2Config, err := NewOAuth2Config(config)
	require.NoError(t, err)

	// First key should be the signing key
	assert.Equal(t, "key-1", oauth2Config.SigningKey.KeyID)

	// All keys should be in JWKS
	assert.Len(t, oauth2Config.SigningJWKS.Keys, 3)
}

func TestNewOAuth2Config_InvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *Config
		wantErr string
	}{
		{
			name: "missing issuer",
			config: &Config{
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				Secret:               []byte("test-secret-with-32-bytes-long!!"),
				PrivateKeys: []PrivateKey{
					{KeyID: "key-1", Algorithm: "RS256", Key: func() *rsa.PrivateKey {
						k, _ := rsa.GenerateKey(rand.Reader, 2048)
						return k
					}()},
				},
			},
			wantErr: "issuer is required",
		},
		{
			name: "no private keys",
			config: &Config{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				Secret:               []byte("test-secret-with-32-bytes-long!!"),
				PrivateKeys:          []PrivateKey{},
			},
			wantErr: "at least one private key is required",
		},
		{
			name: "secret too short",
			config: &Config{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				Secret:               []byte("too-short"),
				PrivateKeys: []PrivateKey{
					{KeyID: "key-1", Algorithm: "RS256", Key: func() *rsa.PrivateKey {
						k, _ := rsa.GenerateKey(rand.Reader, 2048)
						return k
					}()},
				},
			},
			wantErr: "secret must be at least 32 bytes",
		},
		{
			name: "missing secret",
			config: &Config{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				Secret:               nil,
				PrivateKeys: []PrivateKey{
					{KeyID: "key-1", Algorithm: "RS256", Key: func() *rsa.PrivateKey {
						k, _ := rsa.GenerateKey(rand.Reader, 2048)
						return k
					}()},
				},
			},
			wantErr: "secret must be at least 32 bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewOAuth2Config(tt.config)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestOAuth2Config_PublicJWKS(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	config := &Config{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		Secret:               []byte("test-secret-with-32-bytes-long!!"),
		PrivateKeys: []PrivateKey{
			{KeyID: "key-1", Algorithm: "RS256", Key: rsaKey},
		},
	}

	oauth2Config, err := NewOAuth2Config(config)
	require.NoError(t, err)

	publicJWKS := oauth2Config.PublicJWKS()
	require.NotNil(t, publicJWKS)
	require.Len(t, publicJWKS.Keys, 1)

	// Verify it's a public key (not private)
	_, ok := publicJWKS.Keys[0].Key.(*rsa.PublicKey)
	assert.True(t, ok, "expected public key, got %T", publicJWKS.Keys[0].Key)
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	validConfig := &Config{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		Secret:               []byte("test-secret-with-32-bytes-long!!"),
		PrivateKeys: []PrivateKey{
			{KeyID: "key-1", Algorithm: "RS256", Key: rsaKey},
		},
	}

	err = validConfig.Validate()
	assert.NoError(t, err)

	// Test with valid upstream config
	configWithUpstream := &Config{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		Secret:               []byte("test-secret-with-32-bytes-long!!"),
		PrivateKeys: []PrivateKey{
			{KeyID: "key-1", Algorithm: "RS256", Key: rsaKey},
		},
		Upstream: UpstreamConfig{
			Issuer:       "https://accounts.google.com",
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			RedirectURI:  "https://auth.example.com/callback",
		},
	}

	err = configWithUpstream.Validate()
	assert.NoError(t, err)

	// Test with invalid upstream config (missing client ID)
	configWithInvalidUpstream := &Config{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		Secret:               []byte("test-secret-with-32-bytes-long!!"),
		PrivateKeys: []PrivateKey{
			{KeyID: "key-1", Algorithm: "RS256", Key: rsaKey},
		},
		Upstream: UpstreamConfig{
			Issuer:       "https://accounts.google.com",
			ClientID:     "", // Missing!
			ClientSecret: "client-secret",
			RedirectURI:  "https://auth.example.com/callback",
		},
	}

	err = configWithInvalidUpstream.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream config")
	assert.Contains(t, err.Error(), "client ID")
}

func TestPrivateKeyValidate(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	weakRSAKey, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecdsaKeyP384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)

	tests := []struct {
		name    string
		pk      PrivateKey
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid RSA RS256",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "RS256", Key: rsaKey},
			wantErr: false,
		},
		{
			name:    "valid RSA RS384",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "RS384", Key: rsaKey},
			wantErr: false,
		},
		{
			name:    "valid RSA RS512",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "RS512", Key: rsaKey},
			wantErr: false,
		},
		{
			name:    "valid ECDSA ES256",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "ES256", Key: ecdsaKey},
			wantErr: false,
		},
		{
			name:    "missing key ID",
			pk:      PrivateKey{KeyID: "", Algorithm: "RS256", Key: rsaKey},
			wantErr: true,
			errMsg:  "key ID is required",
		},
		{
			name:    "missing algorithm",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "", Key: rsaKey},
			wantErr: true,
			errMsg:  "algorithm is required",
		},
		{
			name:    "nil key",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "RS256", Key: nil},
			wantErr: true,
			errMsg:  "key is required",
		},
		{
			name:    "RSA algorithm with ECDSA key",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "RS256", Key: ecdsaKey},
			wantErr: true,
			errMsg:  "RSA algorithm requires *rsa.PrivateKey",
		},
		{
			name:    "ECDSA algorithm with RSA key",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "ES256", Key: rsaKey},
			wantErr: true,
			errMsg:  "ECDSA algorithm requires *ecdsa.PrivateKey",
		},
		{
			name:    "unsupported algorithm",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "HS256", Key: rsaKey},
			wantErr: true,
			errMsg:  "unsupported algorithm",
		},
		{
			name:    "RSA key too small",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "RS256", Key: weakRSAKey},
			wantErr: true,
			errMsg:  "RSA key must be at least 2048 bits",
		},
		{
			name:    "ECDSA curve mismatch ES256 with P-384",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "ES256", Key: ecdsaKeyP384},
			wantErr: true,
			errMsg:  "algorithm ES256 requires curve P-256, got P-384",
		},
		{
			name:    "valid ECDSA ES384",
			pk:      PrivateKey{KeyID: "key-1", Algorithm: "ES384", Key: ecdsaKeyP384},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.pk.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUpstreamConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		uc      UpstreamConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			uc: UpstreamConfig{
				Issuer:       "https://accounts.google.com",
				ClientID:     "client-id",
				ClientSecret: "client-secret",
				Scopes:       []string{"openid", "profile"},
				RedirectURI:  "https://auth.example.com/callback",
			},
			wantErr: false,
		},
		{
			name: "missing issuer",
			uc: UpstreamConfig{
				ClientID:     "client-id",
				ClientSecret: "client-secret",
				RedirectURI:  "https://auth.example.com/callback",
			},
			wantErr: true,
			errMsg:  "upstream issuer is required",
		},
		{
			name: "missing client ID",
			uc: UpstreamConfig{
				Issuer:       "https://accounts.google.com",
				ClientSecret: "client-secret",
				RedirectURI:  "https://auth.example.com/callback",
			},
			wantErr: true,
			errMsg:  "upstream client ID is required",
		},
		{
			name: "missing client secret",
			uc: UpstreamConfig{
				Issuer:      "https://accounts.google.com",
				ClientID:    "client-id",
				RedirectURI: "https://auth.example.com/callback",
			},
			wantErr: true,
			errMsg:  "upstream client secret is required",
		},
		{
			name: "missing redirect URI",
			uc: UpstreamConfig{
				Issuer:       "https://accounts.google.com",
				ClientID:     "client-id",
				ClientSecret: "client-secret",
			},
			wantErr: true,
			errMsg:  "upstream redirect URI is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.uc.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()

	assert.Equal(t, time.Hour, config.AccessTokenLifespan)
	assert.Equal(t, time.Hour*24*7, config.RefreshTokenLifespan)
	assert.Equal(t, time.Minute*10, config.AuthCodeLifespan)
}
