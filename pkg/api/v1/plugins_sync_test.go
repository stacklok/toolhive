// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/plugins"
	plugmocks "github.com/stacklok/toolhive/pkg/plugins/mocks"
)

// pluginServiceWithSync wraps a mocked PluginService and adds Sync and Upgrade
// methods, so PluginsRouter's opportunistic PluginLockService type assertion
// succeeds — the same shape pluginsvc.New's concrete service has.
type pluginServiceWithSync struct {
	plugins.PluginService
	syncFn    func(ctx context.Context, opts plugins.SyncOptions) (*plugins.SyncResult, error)
	upgradeFn func(ctx context.Context, opts plugins.UpgradeOptions) (*plugins.UpgradeResult, error)
}

func (s *pluginServiceWithSync) Sync(ctx context.Context, opts plugins.SyncOptions) (*plugins.SyncResult, error) {
	return s.syncFn(ctx, opts)
}

func (s *pluginServiceWithSync) Upgrade(ctx context.Context, opts plugins.UpgradeOptions) (*plugins.UpgradeResult, error) {
	if s.upgradeFn == nil {
		return &plugins.UpgradeResult{}, nil
	}
	return s.upgradeFn(ctx, opts)
}

func TestSyncPluginsEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		service      plugins.PluginService
		body         string
		wantStatus   int
		wantContains string
	}{
		{
			name: "successful sync returns 200 with result",
			service: &pluginServiceWithSync{
				PluginService: plugmocks.NewMockPluginService(gomock.NewController(t)),
				syncFn: func(_ context.Context, opts plugins.SyncOptions) (*plugins.SyncResult, error) {
					assert.Equal(t, "/tmp/proj", opts.ProjectRoot)
					assert.True(t, opts.Check)
					return &plugins.SyncResult{AlreadyCurrent: []string{"my-plugin"}}, nil
				},
			},
			body:         `{"project_root":"/tmp/proj","check":true}`,
			wantStatus:   http.StatusOK,
			wantContains: `"my-plugin"`,
		},
		{
			name:       "service without Sync support returns 501",
			service:    plugmocks.NewMockPluginService(gomock.NewController(t)),
			body:       `{"project_root":"/tmp/proj"}`,
			wantStatus: http.StatusNotImplemented,
		},
		{
			name: "invalid JSON body returns 400",
			service: &pluginServiceWithSync{
				PluginService: plugmocks.NewMockPluginService(gomock.NewController(t)),
				syncFn: func(context.Context, plugins.SyncOptions) (*plugins.SyncResult, error) {
					t.Fatal("Sync must not be called for an invalid body")
					return nil, nil
				},
			},
			body:       `{`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "sync error is forwarded",
			service: &pluginServiceWithSync{
				PluginService: plugmocks.NewMockPluginService(gomock.NewController(t)),
				syncFn: func(context.Context, plugins.SyncOptions) (*plugins.SyncResult, error) {
					return nil, httperr.WithCode(assert.AnError, http.StatusForbidden)
				},
			},
			body:       `{"project_root":"/tmp/proj"}`,
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/sync", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			PluginsRouter(tt.service).ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantContains != "" {
				assert.Contains(t, rec.Body.String(), tt.wantContains)
			}
			if rec.Code == http.StatusOK {
				var result plugins.SyncResult
				require.NoError(t, json.NewDecoder(rec.Body).Decode(&result))
			}
		})
	}
}
