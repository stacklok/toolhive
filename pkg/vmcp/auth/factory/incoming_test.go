package factory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestNewIncomingAuthMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		cfg             *config.IncomingAuthConfig
		wantErr         bool
		errContains     string
		checkMiddleware func(t *testing.T, middleware func(http.Handler) http.Handler, authInfo http.Handler)
	}{
		{
			name:        "nil_config_returns_error",
			cfg:         nil,
			wantErr:     true,
			errContains: "incoming auth config is required",
		},
		{
			name: "oidc_missing_config_returns_error",
			cfg: &config.IncomingAuthConfig{
				Type: "oidc",
				OIDC: nil,
			},
			wantErr:     true,
			errContains: "OIDC configuration required",
		},
		{
			name: "local_auth_succeeds",
			cfg: &config.IncomingAuthConfig{
				Type: "local",
			},
			wantErr: false,
			checkMiddleware: func(t *testing.T, middleware func(http.Handler) http.Handler, authInfo http.Handler) {
				t.Helper()

				require.NotNil(t, middleware, "middleware should not be nil")
				assert.Nil(t, authInfo, "local auth should not have authInfo handler")

				// Test that middleware creates identity
				testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					identity, ok := pkgauth.IdentityFromContext(r.Context())
					require.True(t, ok, "identity should be in context")
					require.NotNil(t, identity, "identity should not be nil")
					assert.NotEmpty(t, identity.Subject, "identity subject should not be empty")
					w.WriteHeader(http.StatusOK)
				})

				wrapped := middleware(testHandler)
				req := httptest.NewRequest(http.MethodGet, "/test", nil)
				recorder := httptest.NewRecorder()
				wrapped.ServeHTTP(recorder, req)

				assert.Equal(t, http.StatusOK, recorder.Code)
			},
		},
		{
			name: "anonymous_auth_succeeds",
			cfg: &config.IncomingAuthConfig{
				Type: "anonymous",
			},
			wantErr: false,
			checkMiddleware: func(t *testing.T, middleware func(http.Handler) http.Handler, authInfo http.Handler) {
				t.Helper()

				require.NotNil(t, middleware, "middleware should not be nil")
				assert.Nil(t, authInfo, "anonymous auth should not have authInfo handler")

				// Test that middleware creates identity
				testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					identity, ok := pkgauth.IdentityFromContext(r.Context())
					require.True(t, ok, "identity should be in context")
					require.NotNil(t, identity, "identity should not be nil")
					assert.Equal(t, "anonymous", identity.Subject, "anonymous user should have 'anonymous' subject")
					w.WriteHeader(http.StatusOK)
				})

				wrapped := middleware(testHandler)
				req := httptest.NewRequest(http.MethodGet, "/test", nil)
				recorder := httptest.NewRecorder()
				wrapped.ServeHTTP(recorder, req)

				assert.Equal(t, http.StatusOK, recorder.Code)
			},
		},
		{
			name: "unsupported_auth_type_returns_error",
			cfg: &config.IncomingAuthConfig{
				Type: "unsupported-type",
			},
			wantErr:     true,
			errContains: "unsupported incoming auth type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			middleware, authInfo, err := NewIncomingAuthMiddleware(ctx, tt.cfg)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, middleware)
				assert.Nil(t, authInfo)
			} else {
				require.NoError(t, err)
				require.NotNil(t, middleware)
				if tt.checkMiddleware != nil {
					tt.checkMiddleware(t, middleware, authInfo)
				}
			}
		})
	}
}
