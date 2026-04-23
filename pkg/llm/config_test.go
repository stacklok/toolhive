// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"testing"
)

func TestLLMConfig_IsConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  LLMConfig
		want bool
	}{
		{
			name: "fully configured",
			cfg: LLMConfig{
				GatewayURL: "https://llm.example.com",
				OIDC: LLMOIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			want: true,
		},
		{
			name: "missing gateway URL",
			cfg: LLMConfig{
				OIDC: LLMOIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			want: false,
		},
		{
			name: "missing issuer",
			cfg: LLMConfig{
				GatewayURL: "https://llm.example.com",
				OIDC: LLMOIDCConfig{
					ClientID: "my-client",
				},
			},
			want: false,
		},
		{
			name: "missing client ID",
			cfg: LLMConfig{
				GatewayURL: "https://llm.example.com",
				OIDC: LLMOIDCConfig{
					Issuer: "https://auth.example.com",
				},
			},
			want: false,
		},
		{
			name: "empty config",
			cfg:  LLMConfig{},
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

func TestLLMConfig_Validate(t *testing.T) {
	t.Parallel()

	valid := LLMConfig{
		GatewayURL: "https://llm.example.com",
		OIDC: LLMOIDCConfig{
			Issuer:   "https://auth.example.com",
			ClientID: "my-client",
		},
	}

	tests := []struct {
		name    string
		cfg     LLMConfig
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     valid,
			wantErr: false,
		},
		{
			name: "missing gateway URL",
			cfg: LLMConfig{
				OIDC: LLMOIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			wantErr: true,
		},
		{
			name: "HTTP gateway URL rejected",
			cfg: LLMConfig{
				GatewayURL: "http://llm.example.com",
				OIDC: LLMOIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
			},
			wantErr: true,
		},
		{
			name: "missing issuer",
			cfg: LLMConfig{
				GatewayURL: "https://llm.example.com",
				OIDC: LLMOIDCConfig{
					ClientID: "my-client",
				},
			},
			wantErr: true,
		},
		{
			name: "missing client ID",
			cfg: LLMConfig{
				GatewayURL: "https://llm.example.com",
				OIDC: LLMOIDCConfig{
					Issuer: "https://auth.example.com",
				},
			},
			wantErr: true,
		},
		{
			name: "proxy port below range",
			cfg: LLMConfig{
				GatewayURL: "https://llm.example.com",
				OIDC: LLMOIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
				Proxy: LLMProxyConfig{ListenPort: 80},
			},
			wantErr: true,
		},
		{
			name: "proxy port above range",
			cfg: LLMConfig{
				GatewayURL: "https://llm.example.com",
				OIDC: LLMOIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
				Proxy: LLMProxyConfig{ListenPort: 99999},
			},
			wantErr: true,
		},
		{
			name: "valid custom proxy port",
			cfg: LLMConfig{
				GatewayURL: "https://llm.example.com",
				OIDC: LLMOIDCConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "my-client",
				},
				Proxy: LLMProxyConfig{ListenPort: 8080},
			},
			wantErr: false,
		},
		{
			name: "callback port below range",
			cfg: LLMConfig{
				GatewayURL: "https://llm.example.com",
				OIDC: LLMOIDCConfig{
					Issuer:       "https://auth.example.com",
					ClientID:     "my-client",
					CallbackPort: 100,
				},
			},
			wantErr: true,
		},
		{
			name: "valid callback port",
			cfg: LLMConfig{
				GatewayURL: "https://llm.example.com",
				OIDC: LLMOIDCConfig{
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

func TestLLMConfig_EffectiveProxyPort(t *testing.T) {
	t.Parallel()

	t.Run("returns default when not set", func(t *testing.T) {
		t.Parallel()
		cfg := LLMConfig{}
		if got := cfg.EffectiveProxyPort(); got != DefaultProxyListenPort {
			t.Errorf("EffectiveProxyPort() = %d, want %d", got, DefaultProxyListenPort)
		}
	})

	t.Run("returns configured port", func(t *testing.T) {
		t.Parallel()
		cfg := LLMConfig{Proxy: LLMProxyConfig{ListenPort: 8080}}
		if got := cfg.EffectiveProxyPort(); got != 8080 {
			t.Errorf("EffectiveProxyPort() = %d, want 8080", got)
		}
	})
}

func TestLLMOIDCConfig_EffectiveScopes(t *testing.T) {
	t.Parallel()

	t.Run("returns defaults when not set", func(t *testing.T) {
		t.Parallel()
		cfg := LLMOIDCConfig{}
		scopes := cfg.EffectiveScopes()
		if len(scopes) == 0 {
			t.Error("EffectiveScopes() returned empty slice for zero-value config")
		}
	})

	t.Run("returns configured scopes", func(t *testing.T) {
		t.Parallel()
		cfg := LLMOIDCConfig{Scopes: []string{"openid", "email"}}
		scopes := cfg.EffectiveScopes()
		if len(scopes) != 2 || scopes[0] != "openid" || scopes[1] != "email" {
			t.Errorf("EffectiveScopes() = %v, want [openid email]", scopes)
		}
	})
}
