// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// ── SetFields ────────────────────────────────────────────────────────────────

func TestConfig_SetFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		base    Config
		opts    SetOptions
		want    Config
		wantErr bool
	}{
		{
			name: "sets all fields",
			opts: SetOptions{
				GatewayURL:   "https://gw.example.com",
				Issuer:       "https://auth.example.com",
				ClientID:     "client1",
				Audience:     "aud1",
				ProxyPort:    9000,
				CallbackPort: 9001,
			},
			want: Config{
				GatewayURL: "https://gw.example.com",
				OIDC: OIDCConfig{
					Issuer:       "https://auth.example.com",
					ClientID:     "client1",
					Audience:     "aud1",
					CallbackPort: 9001,
				},
				Proxy: ProxyConfig{ListenPort: 9000},
			},
		},
		{
			name: "zero options leave existing fields untouched",
			base: Config{
				GatewayURL: "https://gw.example.com",
				OIDC:       OIDCConfig{Issuer: "https://auth.example.com", ClientID: "client1"},
			},
			opts: SetOptions{},
			want: Config{
				GatewayURL: "https://gw.example.com",
				OIDC:       OIDCConfig{Issuer: "https://auth.example.com", ClientID: "client1"},
			},
		},
		{
			name: "partial config runs partial validation — valid partial accepted",
			opts: SetOptions{GatewayURL: "https://gw.example.com"},
			want: Config{GatewayURL: "https://gw.example.com"},
		},
		{
			name:    "partial config runs partial validation — HTTP URL rejected",
			opts:    SetOptions{GatewayURL: "http://gw.example.com"},
			wantErr: true,
		},
		{
			name: "full config runs full validation — valid config accepted",
			opts: SetOptions{
				GatewayURL: "https://gw.example.com",
				Issuer:     "https://auth.example.com",
				ClientID:   "client1",
			},
			want: Config{
				GatewayURL: "https://gw.example.com",
				OIDC:       OIDCConfig{Issuer: "https://auth.example.com", ClientID: "client1"},
			},
		},
		{
			name:    "full config runs full validation — invalid issuer rejected",
			opts:    SetOptions{GatewayURL: "https://gw.example.com", Issuer: "not-a-url", ClientID: "c"},
			wantErr: true,
		},
		{
			name: "out-of-range proxy port rejected during partial validation",
			opts: SetOptions{
				GatewayURL: "https://gw.example.com",
				ProxyPort:  80,
			},
			wantErr: true,
		},
		{
			name: "TLSSkipVerify pointer true sets field",
			opts: SetOptions{TLSSkipVerify: boolPtr(true)},
			want: Config{TLSSkipVerify: true},
		},
		{
			name: "TLSSkipVerify pointer false clears field",
			base: Config{
				GatewayURL:    "https://gw.example.com",
				TLSSkipVerify: true,
			},
			opts: SetOptions{TLSSkipVerify: boolPtr(false)},
			want: Config{GatewayURL: "https://gw.example.com", TLSSkipVerify: false},
		},
		{
			name: "nil TLSSkipVerify pointer leaves existing value unchanged",
			base: Config{
				GatewayURL:    "https://gw.example.com",
				TLSSkipVerify: true,
			},
			opts: SetOptions{},
			want: Config{GatewayURL: "https://gw.example.com", TLSSkipVerify: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.base
			err := cfg.SetFields(tt.opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("SetFields() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if cfg.GatewayURL != tt.want.GatewayURL {
				t.Errorf("GatewayURL = %q, want %q", cfg.GatewayURL, tt.want.GatewayURL)
			}
			if cfg.OIDC.Issuer != tt.want.OIDC.Issuer {
				t.Errorf("OIDC.Issuer = %q, want %q", cfg.OIDC.Issuer, tt.want.OIDC.Issuer)
			}
			if cfg.OIDC.ClientID != tt.want.OIDC.ClientID {
				t.Errorf("OIDC.ClientID = %q, want %q", cfg.OIDC.ClientID, tt.want.OIDC.ClientID)
			}
			if cfg.OIDC.Audience != tt.want.OIDC.Audience {
				t.Errorf("OIDC.Audience = %q, want %q", cfg.OIDC.Audience, tt.want.OIDC.Audience)
			}
			if cfg.Proxy.ListenPort != tt.want.Proxy.ListenPort {
				t.Errorf("Proxy.ListenPort = %d, want %d", cfg.Proxy.ListenPort, tt.want.Proxy.ListenPort)
			}
			if cfg.OIDC.CallbackPort != tt.want.OIDC.CallbackPort {
				t.Errorf("OIDC.CallbackPort = %d, want %d", cfg.OIDC.CallbackPort, tt.want.OIDC.CallbackPort)
			}
			if cfg.TLSSkipVerify != tt.want.TLSSkipVerify {
				t.Errorf("TLSSkipVerify = %v, want %v", cfg.TLSSkipVerify, tt.want.TLSSkipVerify)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// ── DeleteCachedTokens ───────────────────────────────────────────────────────

func TestDeleteCachedTokens(t *testing.T) {
	t.Parallel()

	// llmScopeKey returns the fully-scoped key for an LLM secret, matching the
	// __thv_llm_ prefix that ScopedProvider adds.
	llmScopeKey := func(name string) string {
		return secrets.SystemKeyPrefix + string(secrets.ScopeLLM) + "_" + name
	}

	tests := []struct {
		name      string
		caps      secrets.ProviderCapabilities
		setupMock func(m *secretsmocks.MockProvider)
		wantErr   bool
	}{
		{
			name: "no-op when provider cannot list",
			caps: secrets.ProviderCapabilities{CanRead: true, CanWrite: true, CanDelete: true, CanList: false},
		},
		{
			name: "no-op when provider cannot delete",
			caps: secrets.ProviderCapabilities{CanRead: true, CanWrite: true, CanDelete: false, CanList: true},
		},
		{
			name: "no-op when no secrets exist under LLM scope",
			caps: secrets.ProviderCapabilities{CanRead: true, CanWrite: true, CanDelete: true, CanList: true},
			setupMock: func(m *secretsmocks.MockProvider) {
				// Provider returns secrets from other scopes — LLM scope is empty.
				m.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{
					{Key: "__thv_registry_token"},
				}, nil)
			},
		},
		{
			name: "deletes all secrets under LLM scope",
			caps: secrets.ProviderCapabilities{CanRead: true, CanWrite: true, CanDelete: true, CanList: true},
			setupMock: func(m *secretsmocks.MockProvider) {
				m.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{
					{Key: llmScopeKey("refresh_token")},
					{Key: llmScopeKey("access_token")},
					{Key: "__thv_registry_token"}, // different scope, must be ignored
				}, nil)
				m.EXPECT().DeleteSecrets(gomock.Any(), gomock.InAnyOrder([]string{
					llmScopeKey("refresh_token"),
					llmScopeKey("access_token"),
				})).Return(nil)
			},
		},
		{
			name: "propagates ListSecrets error",
			caps: secrets.ProviderCapabilities{CanRead: true, CanWrite: true, CanDelete: true, CanList: true},
			setupMock: func(m *secretsmocks.MockProvider) {
				m.EXPECT().ListSecrets(gomock.Any()).Return(nil, errors.New("storage unavailable"))
			},
			wantErr: true,
		},
		{
			name: "propagates DeleteSecrets error",
			caps: secrets.ProviderCapabilities{CanRead: true, CanWrite: true, CanDelete: true, CanList: true},
			setupMock: func(m *secretsmocks.MockProvider) {
				m.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{
					{Key: llmScopeKey("refresh_token")},
				}, nil)
				m.EXPECT().DeleteSecrets(gomock.Any(), gomock.Any()).Return(errors.New("delete failed"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			mock := secretsmocks.NewMockProvider(ctrl)
			mock.EXPECT().Capabilities().Return(tt.caps).AnyTimes()
			if tt.setupMock != nil {
				tt.setupMock(mock)
			}

			err := DeleteCachedTokens(context.Background(), mock)
			if (err != nil) != tt.wantErr {
				t.Errorf("DeleteCachedTokens() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ── Show ─────────────────────────────────────────────────────────────────────

func TestConfig_Show(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		contains []string
		absent   []string
	}{
		{
			name:     "not-configured message when empty",
			cfg:      Config{},
			contains: []string{"not configured"},
		},
		{
			name: "shows required fields when configured",
			cfg: Config{
				GatewayURL: "https://gw.example.com",
				OIDC:       OIDCConfig{Issuer: "https://auth.example.com", ClientID: "client1"},
			},
			contains: []string{"https://gw.example.com", "https://auth.example.com", "client1"},
			absent:   []string{"not configured"},
		},
		{
			name: "audience shown only when set",
			cfg: Config{
				GatewayURL: "https://gw.example.com",
				OIDC:       OIDCConfig{Issuer: "https://auth.example.com", ClientID: "client1", Audience: "myaud"},
			},
			contains: []string{"myaud"},
		},
		{
			name: "audience absent when not set",
			cfg: Config{
				GatewayURL: "https://gw.example.com",
				OIDC:       OIDCConfig{Issuer: "https://auth.example.com", ClientID: "client1"},
			},
			absent: []string{"Audience"},
		},
		{
			name: "configured tools listed when present",
			cfg: Config{
				GatewayURL:      "https://gw.example.com",
				OIDC:            OIDCConfig{Issuer: "https://auth.example.com", ClientID: "client1"},
				ConfiguredTools: []ToolConfig{{Tool: "cursor", Mode: "proxy", ConfigPath: "/home/user/.cursor/config.json"}},
			},
			contains: []string{"cursor", "proxy", "/home/user/.cursor/config.json"},
		},
		{
			name: "TLS skip verify shown with warning when set",
			cfg: Config{
				GatewayURL:    "https://gw.example.com",
				TLSSkipVerify: true,
				OIDC:          OIDCConfig{Issuer: "https://auth.example.com", ClientID: "client1"},
			},
			contains: []string{"TLS Skip Verify", "true", "WARNING"},
		},
		{
			name: "TLS skip verify not shown when false",
			cfg: Config{
				GatewayURL:    "https://gw.example.com",
				TLSSkipVerify: false,
				OIDC:          OIDCConfig{Issuer: "https://auth.example.com", ClientID: "client1"},
			},
			absent: []string{"TLS Skip Verify"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := tt.cfg.Show(&buf); err != nil {
				t.Fatalf("Show() returned unexpected error: %v", err)
			}
			out := buf.String()
			for _, want := range tt.contains {
				if !strings.Contains(out, want) {
					t.Errorf("Show() output missing %q\ngot: %s", want, out)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(out, absent) {
					t.Errorf("Show() output should not contain %q\ngot: %s", absent, out)
				}
			}
		})
	}
}
