// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"testing"
)

func TestConfig_IsConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "fully configured",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				OIDC: OIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			want: true,
		},
		{
			name: "missing gateway URL",
			cfg: Config{
				OIDC: OIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			want: false,
		},
		{
			name: "missing issuer",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				OIDC: OIDCConfig{
					ClientID: "my-client",
				},
			},
			want: false,
		},
		{
			name: "missing client ID",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				OIDC: OIDCConfig{
					Issuer: "https://auth.example.com",
				},
			},
			want: false,
		},
		{
			name: "empty config",
			cfg:  Config{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.cfg.IsConfigured()
			if got != tt.want {
				t.Errorf("IsConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	valid := Config{
		GatewayURL: "https://llm.example.com",
		OIDC: OIDCConfig{
			Issuer:   "https://auth.example.com",
			ClientID: "my-client",
		},
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     valid,
			wantErr: false,
		},
		{
			name: "missing gateway URL",
			cfg: Config{
				OIDC: OIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			wantErr: true,
		},
		{
			name: "HTTP gateway URL rejected",
			cfg: Config{
				GatewayURL: "http://llm.example.com",
				OIDC: OIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			wantErr: true,
		},
		{
			name: "missing issuer",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				OIDC: OIDCConfig{
					ClientID: "my-client",
				},
			},
			wantErr: true,
		},
		{
			name: "missing client ID",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				OIDC: OIDCConfig{
					Issuer: "https://auth.example.com",
				},
			},
			wantErr: true,
		},
		{
			name: "proxy port below range",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				OIDC: OIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
				Proxy: ProxyConfig{ListenPort: 80},
			},
			wantErr: true,
		},
		{
			name: "proxy port above range",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				OIDC: OIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
				Proxy: ProxyConfig{ListenPort: 99999},
			},
			wantErr: true,
		},
		{
			name: "valid custom proxy port",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				OIDC: OIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
				Proxy: ProxyConfig{ListenPort: 8080},
			},
			wantErr: false,
		},
		{
			name: "callback port below range",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				OIDC: OIDCConfig{
					Issuer:       "https://auth.example.com",
					ClientID:     "my-client",
					CallbackPort: 100,
				},
			},
			wantErr: true,
		},
		{
			name: "valid callback port",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				OIDC: OIDCConfig{
					Issuer:       "https://auth.example.com",
					ClientID:     "my-client",
					CallbackPort: 9000,
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_ValidatePartial(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "empty config is valid",
			cfg:     Config{},
			wantErr: false,
		},
		{
			name:    "valid gateway URL only",
			cfg:     Config{GatewayURL: "https://llm.example.com"},
			wantErr: false,
		},
		{
			name:    "HTTP gateway URL rejected",
			cfg:     Config{GatewayURL: "http://llm.example.com"},
			wantErr: true,
		},
		{
			name:    "valid issuer only",
			cfg:     Config{OIDC: OIDCConfig{Issuer: "https://auth.example.com"}},
			wantErr: false,
		},
		{
			name:    "invalid issuer rejected",
			cfg:     Config{OIDC: OIDCConfig{Issuer: "not-a-url"}},
			wantErr: true,
		},
		{
			name:    "proxy port below range rejected",
			cfg:     Config{Proxy: ProxyConfig{ListenPort: 80}},
			wantErr: true,
		},
		{
			name:    "proxy port above range rejected",
			cfg:     Config{Proxy: ProxyConfig{ListenPort: 99999}},
			wantErr: true,
		},
		{
			name:    "valid proxy port accepted",
			cfg:     Config{Proxy: ProxyConfig{ListenPort: 8080}},
			wantErr: false,
		},
		{
			name:    "callback port below range rejected",
			cfg:     Config{OIDC: OIDCConfig{CallbackPort: 100}},
			wantErr: true,
		},
		{
			name:    "valid callback port accepted",
			cfg:     Config{OIDC: OIDCConfig{CallbackPort: 9000}},
			wantErr: false,
		},
		{
			name: "multiple invalid fields all reported",
			cfg: Config{
				GatewayURL: "http://llm.example.com",
				Proxy:      ProxyConfig{ListenPort: 80},
			},
			wantErr: true,
		},
		{
			name: "required fields absent but valid values accepted",
			cfg: Config{
				GatewayURL: "https://llm.example.com",
				Proxy:      ProxyConfig{ListenPort: 8080},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.ValidatePartial()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePartial() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_EffectiveProxyPort(t *testing.T) {
	t.Parallel()

	t.Run("returns default when not set", func(t *testing.T) {
		t.Parallel()
		cfg := Config{}
		if got := cfg.EffectiveProxyPort(); got != DefaultProxyListenPort {
			t.Errorf("EffectiveProxyPort() = %d, want %d", got, DefaultProxyListenPort)
		}
	})

	t.Run("returns configured port", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Proxy: ProxyConfig{ListenPort: 8080}}
		if got := cfg.EffectiveProxyPort(); got != 8080 {
			t.Errorf("EffectiveProxyPort() = %d, want 8080", got)
		}
	})
}

func TestOIDCConfig_EffectiveScopes(t *testing.T) {
	t.Parallel()

	t.Run("returns defaults when not set", func(t *testing.T) {
		t.Parallel()
		cfg := OIDCConfig{}
		scopes := cfg.EffectiveScopes()
		if len(scopes) == 0 {
			t.Error("EffectiveScopes() returned empty slice for zero-value config")
		}
	})

	t.Run("returns configured scopes", func(t *testing.T) {
		t.Parallel()
		cfg := OIDCConfig{Scopes: []string{"openid", "email"}}
		scopes := cfg.EffectiveScopes()
		if len(scopes) != 2 || scopes[0] != "openid" || scopes[1] != "email" {
			t.Errorf("EffectiveScopes() = %v, want [openid email]", scopes)
		}
	})
}
