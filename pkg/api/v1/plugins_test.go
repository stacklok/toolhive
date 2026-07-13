// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/plugins"
	plugmocks "github.com/stacklok/toolhive/pkg/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
)

func TestPluginsRouter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		setupMock      func(*plugmocks.MockPluginService, string)
		expectedStatus int
		expectedBody   string
	}{
		// listPlugins
		{
			name:   "list plugins success empty",
			method: "GET",
			path:   "/",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().List(gomock.Any(), plugins.ListOptions{}).
					Return([]plugins.InstalledPlugin{}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"plugins":[]}`,
		},
		{
			name:   "list plugins success with results",
			method: "GET",
			path:   "/",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().List(gomock.Any(), plugins.ListOptions{}).
					Return([]plugins.InstalledPlugin{
						{
							Metadata:    plugins.PluginMetadata{Name: "my-plugin"},
							Scope:       plugins.ScopeUser,
							Status:      plugins.InstallStatusInstalled,
							InstalledAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
						},
					}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"my-plugin"`,
		},
		{
			name:   "list plugins project scope missing project root",
			method: "GET",
			path:   "/?scope=project",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().List(gomock.Any(), plugins.ListOptions{
					Scope: plugins.ScopeProject,
				}).Return(nil, httperr.WithCode(
					fmt.Errorf("project_root is required for project scope"),
					http.StatusBadRequest,
				))
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "project_root is required",
		},
		{
			name:   "list plugins with project root filter",
			method: "GET",
			path:   "/?scope=project&project_root={{project_root}}",
			setupMock: func(svc *plugmocks.MockPluginService, projectRoot string) {
				svc.EXPECT().List(gomock.Any(), plugins.ListOptions{
					Scope:       plugins.ScopeProject,
					ProjectRoot: projectRoot,
				}).Return([]plugins.InstalledPlugin{}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"plugins":[]}`,
		},
		{
			name:   "list plugins with client filter",
			method: "GET",
			path:   "/?client=claude-code",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().List(gomock.Any(), plugins.ListOptions{ClientApp: "claude-code"}).
					Return([]plugins.InstalledPlugin{}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"plugins":[]}`,
		},
		{
			name:   "list plugins with group filter",
			method: "GET",
			path:   "/?group=my-group",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().List(gomock.Any(), plugins.ListOptions{Group: "my-group"}).
					Return([]plugins.InstalledPlugin{}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"plugins":[]}`,
		},
		{
			name:   "list plugins error",
			method: "GET",
			path:   "/",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().List(gomock.Any(), gomock.Any()).
					Return(nil, fmt.Errorf("database error"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error",
		},
		// installPlugin
		{
			name:   "install plugin success",
			method: "POST",
			path:   "/",
			body:   `{"name":"my-plugin"}`,
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Install(gomock.Any(), plugins.InstallOptions{Name: "my-plugin"}).
					Return(&plugins.InstallResult{
						Plugin: plugins.InstalledPlugin{
							Metadata:    plugins.PluginMetadata{Name: "my-plugin"},
							Scope:       plugins.ScopeUser,
							Status:      plugins.InstallStatusPending,
							InstalledAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
						},
					}, nil)
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   `"my-plugin"`,
		},
		{
			name:   "install plugin empty name",
			method: "POST",
			path:   "/",
			body:   `{"name":""}`,
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Install(gomock.Any(), plugins.InstallOptions{Name: ""}).
					Return(nil, httperr.WithCode(fmt.Errorf("invalid plugin name: must not be empty"), http.StatusBadRequest))
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid plugin name",
		},
		{
			name:   "install plugin missing name field",
			method: "POST",
			path:   "/",
			body:   `{}`,
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Install(gomock.Any(), plugins.InstallOptions{Name: ""}).
					Return(nil, httperr.WithCode(fmt.Errorf("invalid plugin name: must not be empty"), http.StatusBadRequest))
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid plugin name",
		},
		{
			name:           "install plugin malformed json",
			method:         "POST",
			path:           "/",
			body:           `{invalid`,
			setupMock:      func(_ *plugmocks.MockPluginService, _ string) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid request body",
		},
		{
			name:   "install plugin already exists",
			method: "POST",
			path:   "/",
			body:   `{"name":"my-plugin"}`,
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Install(gomock.Any(), gomock.Any()).
					Return(nil, storage.ErrAlreadyExists)
			},
			expectedStatus: http.StatusConflict,
			expectedBody:   "resource already exists",
		},
		{
			name:   "install plugin with clients",
			method: "POST",
			path:   "/",
			body:   `{"name":"my-plugin","clients":["claude-code","codex"]}`,
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Install(gomock.Any(), plugins.InstallOptions{
					Name:    "my-plugin",
					Clients: []string{"claude-code", "codex"},
				}).Return(&plugins.InstallResult{
					Plugin: plugins.InstalledPlugin{
						Metadata: plugins.PluginMetadata{Name: "my-plugin"},
						Status:   plugins.InstallStatusInstalled,
						Clients:  []string{"claude-code", "codex"},
					},
				}, nil)
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   `"my-plugin"`,
		},
		{
			name:   "install plugin with version and scope",
			method: "POST",
			path:   "/",
			body:   `{"name":"my-plugin","version":"1.2.0","scope":"project","project_root":"{{project_root}}"}`,
			setupMock: func(svc *plugmocks.MockPluginService, projectRoot string) {
				svc.EXPECT().Install(gomock.Any(), plugins.InstallOptions{
					Name:        "my-plugin",
					Version:     "1.2.0",
					Scope:       plugins.ScopeProject,
					ProjectRoot: projectRoot,
				}).Return(&plugins.InstallResult{
					Plugin: plugins.InstalledPlugin{
						Metadata: plugins.PluginMetadata{Name: "my-plugin", Version: "1.2.0"},
						Scope:    plugins.ScopeProject,
						Status:   plugins.InstallStatusPending,
					},
				}, nil)
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   `"my-plugin"`,
		},
		// uninstallPlugin
		{
			name:   "uninstall plugin success",
			method: "DELETE",
			path:   "/my-plugin",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Uninstall(gomock.Any(), plugins.UninstallOptions{Name: "my-plugin"}).
					Return(nil)
			},
			expectedStatus: http.StatusNoContent,
		},
		{
			name:           "uninstall plugin invalid name",
			method:         "DELETE",
			path:           "/A",
			setupMock:      func(_ *plugmocks.MockPluginService, _ string) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid",
		},
		{
			name:   "uninstall plugin not found",
			method: "DELETE",
			path:   "/my-plugin",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Uninstall(gomock.Any(), gomock.Any()).
					Return(storage.ErrNotFound)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "resource not found",
		},
		{
			name:   "uninstall plugin with scope",
			method: "DELETE",
			path:   "/my-plugin?scope=project&project_root={{project_root}}",
			setupMock: func(svc *plugmocks.MockPluginService, projectRoot string) {
				svc.EXPECT().Uninstall(gomock.Any(), plugins.UninstallOptions{
					Name:        "my-plugin",
					Scope:       plugins.ScopeProject,
					ProjectRoot: projectRoot,
				}).Return(nil)
			},
			expectedStatus: http.StatusNoContent,
		},
		// getPluginInfo
		{
			name:   "get plugin info found",
			method: "GET",
			path:   "/my-plugin",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Info(gomock.Any(), plugins.InfoOptions{Name: "my-plugin"}).
					Return(&plugins.PluginInfo{
						Metadata: plugins.PluginMetadata{Name: "my-plugin"},
						InstalledPlugin: &plugins.InstalledPlugin{
							Metadata: plugins.PluginMetadata{Name: "my-plugin"},
							Scope:    plugins.ScopeUser,
							Status:   plugins.InstallStatusInstalled,
						},
					}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"installed_plugin"`,
		},
		{
			name:   "get plugin info not found",
			method: "GET",
			path:   "/my-plugin",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Info(gomock.Any(), plugins.InfoOptions{Name: "my-plugin"}).
					Return(nil, storage.ErrNotFound)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "resource not found",
		},
		{
			name:           "get plugin info invalid name",
			method:         "GET",
			path:           "/A",
			setupMock:      func(_ *plugmocks.MockPluginService, _ string) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid",
		},
		{
			name:   "get plugin info service error",
			method: "GET",
			path:   "/my-plugin",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Info(gomock.Any(), plugins.InfoOptions{Name: "my-plugin"}).
					Return(nil, fmt.Errorf("database error"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error",
		},
		{
			name:   "get plugin info with unmaterialized components and degraded clients",
			method: "GET",
			path:   "/my-plugin",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Info(gomock.Any(), plugins.InfoOptions{Name: "my-plugin"}).
					Return(&plugins.PluginInfo{
						Metadata: plugins.PluginMetadata{Name: "my-plugin"},
						InstalledPlugin: &plugins.InstalledPlugin{
							Metadata: plugins.PluginMetadata{Name: "my-plugin"},
							Scope:    plugins.ScopeProject,
							Status:   plugins.InstallStatusInstalled,
						},
						UnmaterializedComponents: map[string][]plugins.ComponentType{
							"codex": {plugins.ComponentCommands, plugins.ComponentAgents},
						},
						ProjectScopeDegradedClients: []string{"codex"},
					}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"unmaterialized_components"`,
		},
		// validatePlugin
		{
			name:   "validate plugin success",
			method: "POST",
			path:   "/validate",
			body:   `{"path":"/tmp/plugin"}`,
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Validate(gomock.Any(), "/tmp/plugin").
					Return(&plugins.ValidationResult{Valid: true}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"valid":true`,
		},
		{
			name:           "validate plugin bad request",
			method:         "POST",
			path:           "/validate",
			body:           `{invalid`,
			setupMock:      func(_ *plugmocks.MockPluginService, _ string) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid request body",
		},
		{
			name:           "validate plugin empty path",
			method:         "POST",
			path:           "/validate",
			body:           `{"path":""}`,
			setupMock:      func(_ *plugmocks.MockPluginService, _ string) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "path is required",
		},
		{
			name:   "validate plugin service error",
			method: "POST",
			path:   "/validate",
			body:   `{"path":"/tmp/plugin"}`,
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Validate(gomock.Any(), "/tmp/plugin").
					Return(nil, fmt.Errorf("validation failed"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error",
		},
		// buildPlugin
		{
			name:   "build plugin success",
			method: "POST",
			path:   "/build",
			body:   `{"path":"/tmp/plugin","tag":"v1.0.0"}`,
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Build(gomock.Any(), plugins.BuildOptions{Path: "/tmp/plugin", Tag: "v1.0.0"}).
					Return(&plugins.BuildResult{Reference: "v1.0.0"}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"reference":"v1.0.0"`,
		},
		{
			name:           "build plugin bad request",
			method:         "POST",
			path:           "/build",
			body:           `{invalid`,
			setupMock:      func(_ *plugmocks.MockPluginService, _ string) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid request body",
		},
		{
			name:           "build plugin empty path",
			method:         "POST",
			path:           "/build",
			body:           `{"path":"","tag":"v1"}`,
			setupMock:      func(_ *plugmocks.MockPluginService, _ string) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "path is required",
		},
		// pushPlugin
		{
			name:   "push plugin success",
			method: "POST",
			path:   "/push",
			body:   `{"reference":"ghcr.io/test/plugin:v1"}`,
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Push(gomock.Any(), plugins.PushOptions{Reference: "ghcr.io/test/plugin:v1"}).
					Return(nil)
			},
			expectedStatus: http.StatusNoContent,
		},
		{
			name:           "push plugin bad request",
			method:         "POST",
			path:           "/push",
			body:           `{invalid`,
			setupMock:      func(_ *plugmocks.MockPluginService, _ string) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid request body",
		},
		{
			name:           "push plugin empty reference",
			method:         "POST",
			path:           "/push",
			body:           `{"reference":""}`,
			setupMock:      func(_ *plugmocks.MockPluginService, _ string) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "reference is required",
		},
		{
			name:   "push plugin service error",
			method: "POST",
			path:   "/push",
			body:   `{"reference":"ghcr.io/test/plugin:v1"}`,
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().Push(gomock.Any(), plugins.PushOptions{Reference: "ghcr.io/test/plugin:v1"}).
					Return(fmt.Errorf("push failed"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error",
		},
		// listBuilds
		{
			name:   "list builds success empty",
			method: "GET",
			path:   "/builds",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().ListBuilds(gomock.Any()).
					Return([]plugins.LocalBuild{}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `{"builds":[]}`,
		},
		{
			name:   "list builds success with results",
			method: "GET",
			path:   "/builds",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().ListBuilds(gomock.Any()).
					Return([]plugins.LocalBuild{
						{Tag: "my-plugin", Digest: "sha256:abc123", Name: "my-plugin", Version: "1.0.0"},
					}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"tag":"my-plugin"`,
		},
		{
			name:   "list builds service error",
			method: "GET",
			path:   "/builds",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().ListBuilds(gomock.Any()).
					Return(nil, httperr.WithCode(fmt.Errorf("oci store not configured"), http.StatusInternalServerError))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error",
		},
		// deleteBuild
		{
			name:   "delete build success",
			method: "DELETE",
			path:   "/builds/my-plugin",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().DeleteBuild(gomock.Any(), "my-plugin").Return(nil)
			},
			expectedStatus: http.StatusNoContent,
		},
		{
			name:   "delete build not found",
			method: "DELETE",
			path:   "/builds/missing",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().DeleteBuild(gomock.Any(), "missing").
					Return(httperr.WithCode(fmt.Errorf("tag not found"), http.StatusNotFound))
			},
			expectedStatus: http.StatusNotFound,
		},
		// getPluginContent
		{
			name:   "get plugin content success",
			method: "GET",
			path:   "/content?ref=my-plugin",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().GetContent(gomock.Any(), plugins.ContentOptions{Reference: "my-plugin"}).
					Return(&plugins.PluginContent{
						Name:     "my-plugin",
						Manifest: `{"name":"my-plugin"}`,
						Files:    []plugins.PluginFileEntry{{Path: "plugin.json", Size: 42}},
					}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"my-plugin"`,
		},
		{
			name:           "get plugin content missing ref",
			method:         "GET",
			path:           "/content",
			setupMock:      func(_ *plugmocks.MockPluginService, _ string) {},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "ref query parameter is required",
		},
		{
			name:   "get plugin content service error",
			method: "GET",
			path:   "/content?ref=missing",
			setupMock: func(svc *plugmocks.MockPluginService, _ string) {
				svc.EXPECT().GetContent(gomock.Any(), plugins.ContentOptions{Reference: "missing"}).
					Return(nil, storage.ErrNotFound)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "resource not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := tt.path
			body := tt.body
			projectRoot := ""
			if strings.Contains(path, "{{project_root}}") || strings.Contains(body, "{{project_root}}") {
				projectRoot = makeProjectRoot(t)
				path = strings.ReplaceAll(path, "{{project_root}}", url.QueryEscape(projectRoot))
				body = strings.ReplaceAll(body, "{{project_root}}", projectRoot)
			}

			ctrl := gomock.NewController(t)
			mockSvc := plugmocks.NewMockPluginService(ctrl)
			tt.setupMock(mockSvc, projectRoot)

			router := chi.NewRouter()
			router.Mount("/", PluginsRouter(mockSvc))

			req := httptest.NewRequest(tt.method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			if tt.expectedBody != "" {
				assert.Contains(t, rec.Body.String(), tt.expectedBody)
			}
		})
	}
}

func TestPluginsRouterGetInfoJSONKeys(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockSvc := plugmocks.NewMockPluginService(ctrl)

	mockSvc.EXPECT().Info(gomock.Any(), plugins.InfoOptions{Name: "my-plugin"}).
		Return(&plugins.PluginInfo{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			InstalledPlugin: &plugins.InstalledPlugin{
				Metadata: plugins.PluginMetadata{Name: "my-plugin"},
				Scope:    plugins.ScopeProject,
				Status:   plugins.InstallStatusInstalled,
			},
			UnmaterializedComponents: map[string][]plugins.ComponentType{
				"codex": {plugins.ComponentCommands, plugins.ComponentAgents},
			},
			ProjectScopeDegradedClients: []string{"codex"},
		}, nil)

	router := chi.NewRouter()
	router.Mount("/", PluginsRouter(mockSvc))

	req := httptest.NewRequest(http.MethodGet, "/my-plugin", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"unmaterialized_components"`)
	assert.Contains(t, body, `"project_scope_degraded_clients"`)
}

func TestPluginsListResponseFormat(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockSvc := plugmocks.NewMockPluginService(ctrl)

	mockSvc.EXPECT().List(gomock.Any(), gomock.Any()).
		Return([]plugins.InstalledPlugin{
			{
				Metadata:    plugins.PluginMetadata{Name: "plugin-one", Version: "1.0.0"},
				Scope:       plugins.ScopeUser,
				Status:      plugins.InstallStatusInstalled,
				InstalledAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		}, nil)

	router := chi.NewRouter()
	router.Mount("/", PluginsRouter(mockSvc))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"plugins"`)
	assert.Contains(t, rec.Body.String(), `"plugin-one"`)
}

func TestPluginsInstallLocationHeader(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockSvc := plugmocks.NewMockPluginService(ctrl)

	mockSvc.EXPECT().Install(gomock.Any(), plugins.InstallOptions{Name: "my-plugin"}).
		Return(&plugins.InstallResult{
			Plugin: plugins.InstalledPlugin{
				Metadata: plugins.PluginMetadata{Name: "my-plugin"},
				Scope:    plugins.ScopeUser,
				Status:   plugins.InstallStatusInstalled,
			},
		}, nil)

	router := chi.NewRouter()
	router.Mount("/", PluginsRouter(mockSvc))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"my-plugin"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "/api/v1beta/plugins/my-plugin", rec.Header().Get("Location"))
}
