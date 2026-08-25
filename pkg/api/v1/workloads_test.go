// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adrg/xdg"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	apierrors "github.com/stacklok/toolhive/pkg/api/errors"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	runtimemocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/core"
	groupsmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/workloads"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
	wt "github.com/stacklok/toolhive/pkg/workloads/types"
)

func TestGetWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		workloadName   string
		setupMock      func(*workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedStatus int
		expectedBody   string
	}{
		{
			name:         "workload not found",
			workloadName: "nonexistent",
			setupMock: func(wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "nonexistent").
					Return(core.Workload{}, runtime.ErrWorkloadNotFound)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "workload not found",
		},
		{
			name:         "invalid workload name",
			workloadName: "invalid-name",
			setupMock: func(wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "invalid-name").
					Return(core.Workload{}, wt.ErrInvalidWorkloadName)
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid workload name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)
			tt.setupMock(mockWorkloadManager, mockRuntime, mockGroupManager)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				debugMode:        false,
			}

			req := httptest.NewRequest("GET", "/"+tt.workloadName, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", tt.workloadName)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			apierrors.ErrorHandler(routes.getWorkload).ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}

// TestCreateWorkload cannot call t.Parallel() at its own level: two cases
// below compute their expectation via getBaseRuntimeConfig, which reads the
// process-wide config singleton (config.NewProvider() -> getSingletonConfig).
// Fixing that singleton to a known-empty config makes both the test's
// expectation and the production code path deterministic regardless of the
// developer's real ~/.config/toolhive - a configured additional_packages or
// runtime_env would otherwise make the expectation wrong on that machine.
// Subtests still run in parallel with each other; they just all observe the
// same fixed singleton, so there's nothing to race on.
//
//nolint:paralleltest,tparallel // Mutates the process-global config singleton; see comment above.
func TestCreateWorkload(t *testing.T) {
	config.SetSingletonConfig(&config.Config{})
	t.Cleanup(config.ResetSingleton)

	tests := []struct {
		name                  string
		requestBody           string
		setupMock             func(*testing.T, *workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedServerOrImage string
		expectedRuntimeConfig *templates.RuntimeConfig
		expectedStatus        int
		expectedBody          string
	}{
		{
			name:        "invalid JSON",
			requestBody: `{"name":`,
			setupMock: func(_ *testing.T, _ *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "failed to decode request",
		},
		{
			name:        "workload already exists",
			requestBody: `{"name": "existing-workload", "image": "test-image"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "existing-workload").Return(true, nil)
			},
			expectedStatus: http.StatusConflict,
			expectedBody:   "workload with name existing-workload already exists",
		},
		{
			name:        "invalid proxy mode",
			requestBody: `{"name": "test-workload", "image": "test-image", "proxy_mode": "invalid"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil).AnyTimes()
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "Invalid proxy_mode",
		},
		{
			name: "with runtime config override",
			requestBody: `{"name": "test-workload", "image": "go://github.com/example/server", ` +
				`"runtime_config": {"builder_image": "golang:1.24-alpine", "additional_packages": ["curl"]}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().RunWorkloadDetached(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, runConfig *runner.RunConfig) error {
						assert.NotNil(t, runConfig.RuntimeConfig)
						assert.Equal(t, "golang:1.24-alpine", runConfig.RuntimeConfig.BuilderImage)
						assert.Equal(t, []string{"curl"}, runConfig.RuntimeConfig.AdditionalPackages)
						return nil
					})
			},
			expectedRuntimeConfig: func() *templates.RuntimeConfig {
				base := getBaseRuntimeConfig(templates.TransportTypeGO)
				// "curl" is not a Go default on any machine's config, so it
				// must survive the merge regardless of local overrides in
				// ~/.config/toolhive — proves the override is genuinely
				// applied rather than the assertion being self-referential.
				// Dedupe itself is covered hermetically in
				// pkg/container/templates/runtime_config_test.go.
				return &templates.RuntimeConfig{
					BuilderImage:       "golang:1.24-alpine",
					AdditionalPackages: append(append([]string{}, base.AdditionalPackages...), "curl"),
				}
			}(),
			expectedServerOrImage: "go://github.com/example/server",
			expectedStatus:        http.StatusCreated,
			expectedBody:          "test-workload",
		},
		{
			name:        "empty runtime config is ignored",
			requestBody: `{"name": "test-workload", "image": "go://github.com/example/server", "runtime_config": {}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().RunWorkloadDetached(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, runConfig *runner.RunConfig) error {
						assert.Nil(t, runConfig.RuntimeConfig)
						return nil
					})
			},
			expectedServerOrImage: "go://github.com/example/server",
			expectedStatus:        http.StatusCreated,
			expectedBody:          "test-workload",
		},
		{
			name:        "runtime config with non protocol image is rejected",
			requestBody: `{"name": "test-workload", "image": "nginx:latest", "runtime_config": {"builder_image": "golang:1.24-alpine"}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "runtime_config is only supported for protocol-scheme images",
		},
		{
			// build_with is only supported for uvx builds; npx must be
			// rejected with an actionable 400, not a scrubbed 500 (the
			// bug this design closes — see pkg/api/errors/handler.go).
			name:        "npx build_with is rejected with 400, not a scrubbed 500",
			requestBody: `{"name": "test-workload", "image": "npx://some-pkg", "runtime_config": {"build_with": ["mcp<2"]}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "build_with is not supported for npx:// builds",
		},
		{
			// runtime_env-only requests must not slip past the emptiness
			// short-circuit: a request carrying only runtime_env against a
			// non-protocol image must still hit the protocol-scheme guard,
			// not be silently discarded and accepted.
			name:        "runtime_env only, non protocol image is rejected",
			requestBody: `{"name": "test-workload", "image": "nginx:latest", "runtime_config": {"runtime_env": {"NODE_ENV": "production"}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "runtime_config is only supported for protocol-scheme images",
		},
		{
			// The headline bug: build_with (and runtime_env) must actually
			// reach the build, not just be accepted. Pins both sinks fed by
			// runtimeConfigFromRequest: the retriever config (via
			// expectedRuntimeConfig, coming from runtimeConfigForImageBuild's
			// WithOverrides merge) and the persisted config (via the
			// RunWorkloadDetached closure, coming from runtimeConfigFromRequest
			// unmerged) — these are two different functions.
			name: "uvx build_with and runtime_env reach both the build and the persisted config",
			requestBody: `{"name": "test-workload", "image": "uvx://arxiv-mcp-server", ` +
				`"runtime_config": {"build_with": ["mcp<2"], "runtime_env": {"NODE_ENV": "production"}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().RunWorkloadDetached(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, runConfig *runner.RunConfig) error {
						assert.NotNil(t, runConfig.RuntimeConfig)
						assert.Equal(t, []string{"mcp<2"}, runConfig.RuntimeConfig.BuildWith)
						assert.Equal(t, map[string]string{"NODE_ENV": "production"}, runConfig.RuntimeConfig.RuntimeEnv)
						return nil
					})
			},
			expectedRuntimeConfig: func() *templates.RuntimeConfig {
				base := getBaseRuntimeConfig(templates.TransportTypeUVX)
				return &templates.RuntimeConfig{
					BuilderImage:       base.BuilderImage,
					AdditionalPackages: base.AdditionalPackages,
					BuildWith:          []string{"mcp<2"},
					RuntimeEnv:         map[string]string{"NODE_ENV": "production"},
				}
			}(),
			expectedServerOrImage: "uvx://arxiv-mcp-server",
			expectedStatus:        http.StatusCreated,
			expectedBody:          "test-workload",
		},
		{
			name:        "with tool filters",
			requestBody: `{"name": "test-workload", "image": "test-image", "tools": ["filter1", "filter2"]}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string{"filter1", "filter2"}

				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().RunWorkloadDetached(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, runConfig *runner.RunConfig) error {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						return nil
					})
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   "test-workload",
		},
		{
			name:        "with tool override",
			requestBody: `{"name": "test-workload", "image": "test-image", "tools_override": {"actual-tool": {"name": "override-tool", "description": "Overridden tool"}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string(nil)

				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().RunWorkloadDetached(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, runConfig *runner.RunConfig) error {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						return nil
					})
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   "test-workload",
		},
		{
			name:        "with both tool filters and tool override",
			requestBody: `{"name": "test-workload", "image": "test-image", "tools": ["filter1"], "tools_override": {"actual-tool": {"name": "override-tool", "description": "Overridden tool"}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string{"filter1"}

				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().RunWorkloadDetached(gomock.Any(), gomock.Any()).
					DoAndReturn(func(_ context.Context, runConfig *runner.RunConfig) error {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						return nil
					})
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   "test-workload",
		},
		{
			name:        "with bogus tool override",
			requestBody: `{"name": "test-workload", "image": "test-image", "tools_override": {"actual-tool": {"name": "", "description": ""}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().DoesWorkloadExist(gomock.Any(), "test-workload").Return(false, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "tool override for actual-tool must have either Name or Description set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)

			tt.setupMock(t, mockWorkloadManager, mockRuntime, mockGroupManager)
			expectedServerOrImage := tt.expectedServerOrImage
			if expectedServerOrImage == "" {
				expectedServerOrImage = "test-image"
			}

			mockRetriever := makeMockRetriever(t,
				expectedServerOrImage,
				&regtypes.ImageMetadata{Image: "test-image"},
				tt.expectedRuntimeConfig,
			)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				debugMode:        false,
				workloadService: &WorkloadService{
					groupManager:      mockGroupManager,
					workloadManager:   mockWorkloadManager,
					imageRetriever:    mockRetriever,
					imagePuller:       func(_ context.Context, _ string) error { return nil },
					configProvider:    config.NewDefaultProvider(),
					imageVerification: retriever.VerifyImageWarn,
				},
			}

			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			apierrors.ErrorHandler(routes.createWorkload).ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}

func TestUpdateWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		workloadName   string
		requestBody    string
		setupMock      func(*testing.T, *workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedStatus int
		expectedBody   string
	}{
		{
			name:         "invalid JSON",
			workloadName: "test-workload",
			requestBody:  `{"image":`,
			setupMock: func(_ *testing.T, _ *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid JSON",
		},
		{
			name:         "workload not found",
			workloadName: "nonexistent",
			requestBody:  `{"image": "test-image"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, _ *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "nonexistent").
					Return(core.Workload{}, runtime.ErrWorkloadNotFound)
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "workload not found",
		},
		{
			name:         "stop workload fails",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					Return(nil, fmt.Errorf("stop failed"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error", // 5xx errors return generic message
		},
		{
			name:         "delete workload fails",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					Return(nil, fmt.Errorf("delete failed"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedBody:   "Internal Server Error", // 5xx errors return generic message
		},
		{
			name:         "with tool filters",
			workloadName: "test-workload",
			requestBody:  `{"name": "test-workload", "image": "test-image", "tools": ["filter1", "filter2"]}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string{"filter1", "filter2"}
				toolsOverride := map[string]runner.ToolOverride{}

				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (workloads.CompletionFunc, error) {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						assert.Equal(t, toolsOverride, runConfig.ToolsOverride, "Tools override should be equal")
						return nil, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
		},
		{
			name:         "with tool override",
			workloadName: "test-workload",
			requestBody:  `{"name": "test-workload", "image": "test-image", "tools_override": {"actual-tool": {"name": "override-tool", "description": "Overridden tool"}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string(nil)
				toolsOverride := map[string]runner.ToolOverride{
					"actual-tool": {
						Name:        "override-tool",
						Description: "Overridden tool",
					},
				}

				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (workloads.CompletionFunc, error) {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						assert.Equal(t, toolsOverride, runConfig.ToolsOverride, "Tools override should be equal")
						return nil, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
		},
		{
			name:         "with both tool filters and tool override",
			workloadName: "test-workload",
			requestBody:  `{"name": "test-workload", "image": "test-image", "tools": ["filter1"], "tools_override": {"actual-tool": {"name": "override-tool", "description": "Overridden tool"}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				toolsFilter := []string{"filter1"}
				toolsOverride := map[string]runner.ToolOverride{
					"actual-tool": {
						Name:        "override-tool",
						Description: "Overridden tool",
					},
				}

				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (workloads.CompletionFunc, error) {
						assert.Equal(t, toolsFilter, runConfig.ToolsFilter, "Tools filter should be equal")
						assert.Equal(t, toolsOverride, runConfig.ToolsOverride, "Tools override should be equal")
						return nil, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
		},
		{
			name:         "with bogus tool override",
			workloadName: "test-workload",
			requestBody:  `{"name": "test-workload", "image": "test-image", "tools_override": {"actual-tool": {"name": "", "description": ""}}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				// The validation error should occur before UpdateWorkload is called
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "tool override for actual-tool must have either Name or Description set",
		},
		{
			name:         "runtime config omitted on update clears stored override",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image"}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (workloads.CompletionFunc, error) {
						assert.Nil(t, runConfig.RuntimeConfig)
						return nil, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
		},
		{
			name:         "runtime config with non protocol image is rejected",
			workloadName: "test-workload",
			requestBody:  `{"image": "nginx:latest", "runtime_config": {"builder_image": "golang:1.24-alpine"}}`,
			setupMock: func(_ *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload"}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "runtime_config is only supported for protocol-scheme images",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)
			tt.setupMock(t, mockWorkloadManager, mockRuntime, mockGroupManager)

			mockRetriever := makeMockRetriever(t,
				"test-image",
				&regtypes.ImageMetadata{Image: "test-image"},
				nil,
			)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				debugMode:        false,
				workloadService: &WorkloadService{
					groupManager:      mockGroupManager,
					workloadManager:   mockWorkloadManager,
					imageRetriever:    mockRetriever,
					imagePuller:       func(_ context.Context, _ string) error { return nil },
					configProvider:    config.NewDefaultProvider(),
					imageVerification: retriever.VerifyImageWarn,
				},
			}

			req := httptest.NewRequest("POST", "/"+tt.workloadName+"/edit", strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", tt.workloadName)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			assert.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}

// TestUpdateWorkload_ProtocolBuiltRuntimeConfigRoundTrip guards the GET-edit-PUT
// regression: a workload built from a protocol-scheme image (uvx://, npx://,
// go://) persists Image as the *built* image (no longer a protocol scheme)
// plus the RuntimeConfig used to build it. GET echoes both back, and PUT-ing
// that response unchanged must succeed rather than 400 on the protocol-scheme
// guard in runtimeConfigForImageBuild. A genuinely different runtime_config on
// the same non-protocol image must still be rejected.
//
//nolint:paralleltest // SaveState/LoadState use process-wide XDG state settings; keep sequential.
func TestUpdateWorkload_ProtocolBuiltRuntimeConfigRoundTrip(t *testing.T) {
	t.Cleanup(xdg.Reload)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	xdg.Reload()

	ctx := context.Background()
	const workloadName = "test-workload"
	builtImage := "toolhivelocal/uvx-arxiv-mcp-server:20260101000000"

	persisted := runner.NewRunConfig()
	persisted.Name = workloadName
	persisted.BaseName = workloadName
	persisted.ContainerName = workloadName
	persisted.Image = builtImage
	persisted.RuntimeConfig = &templates.RuntimeConfig{
		BuilderImage:       "python:3.14-slim",
		AdditionalPackages: []string{"ca-certificates"},
		BuildWith:          []string{"mcp<2"},
		RuntimeEnv:         map[string]string{"NODE_ENV": "production"},
	}
	require.NoError(t, persisted.SaveState(ctx))

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
	mockRuntime := runtimemocks.NewMockRuntime(ctrl)
	mockGroupManager := groupsmocks.NewMockManager(ctrl)

	routes := &WorkloadRoutes{
		workloadManager:  mockWorkloadManager,
		containerRuntime: mockRuntime,
		groupManager:     mockGroupManager,
		workloadService: &WorkloadService{
			groupManager:      mockGroupManager,
			workloadManager:   mockWorkloadManager,
			imagePuller:       func(_ context.Context, _ string) error { return nil },
			configProvider:    config.NewDefaultProvider(),
			imageVerification: retriever.VerifyImageWarn,
		},
	}

	// GET: fetch the persisted config as JSON, exactly as a client would.
	mockWorkloadManager.EXPECT().GetWorkload(gomock.Any(), workloadName).
		Return(core.Workload{Name: workloadName}, nil)

	getReq := httptest.NewRequest("GET", "/"+workloadName, nil)
	getRctx := chi.NewRouteContext()
	getRctx.URLParams.Add("name", workloadName)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), chi.RouteCtxKey, getRctx))
	getW := httptest.NewRecorder()
	apierrors.ErrorHandler(routes.getWorkload).ServeHTTP(getW, getReq)
	require.Equal(t, http.StatusOK, getW.Code, getW.Body.String())
	getBody := getW.Body.Bytes()

	t.Run("PUT the GET response back unchanged succeeds and preserves the config", func(t *testing.T) {
		mockWorkloadManager.EXPECT().GetWorkload(gomock.Any(), workloadName).
			Return(core.Workload{Name: workloadName}, nil)
		mockGroupManager.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
		mockWorkloadManager.EXPECT().UpdateWorkload(gomock.Any(), workloadName, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (workloads.CompletionFunc, error) {
				assert.NotNil(t, runConfig.RuntimeConfig)
				assert.Equal(t, "python:3.14-slim", runConfig.RuntimeConfig.BuilderImage)
				assert.Equal(t, []string{"ca-certificates"}, runConfig.RuntimeConfig.AdditionalPackages)
				assert.Equal(t, []string{"mcp<2"}, runConfig.RuntimeConfig.BuildWith)
				assert.Equal(t, map[string]string{"NODE_ENV": "production"}, runConfig.RuntimeConfig.RuntimeEnv)
				return nil, nil
			})
		// The runtime_config is an exact echo, so it's suppressed from the
		// retriever/builder input only (nothing to rebuild); the request's
		// runtime_config itself is never cleared, so runConfig.RuntimeConfig
		// (asserted above) is populated from the request throughout.
		routes.workloadService.imageRetriever = makeMockRetriever(t, builtImage, &regtypes.ImageMetadata{Image: builtImage}, nil)

		putReq := httptest.NewRequest("POST", "/"+workloadName+"/edit", bytes.NewReader(getBody))
		putReq.Header.Set("Content-Type", "application/json")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", workloadName)
		putReq = putReq.WithContext(context.WithValue(putReq.Context(), chi.RouteCtxKey, rctx))

		putW := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(putW, putReq)
		assert.Equal(t, http.StatusOK, putW.Code, putW.Body.String())
	})

	t.Run("genuinely different runtime_config on the same non-protocol image still 400s", func(t *testing.T) {
		mockWorkloadManager.EXPECT().GetWorkload(gomock.Any(), workloadName).
			Return(core.Workload{Name: workloadName}, nil)
		mockGroupManager.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)

		body := fmt.Sprintf(`{"image": %q, "runtime_config": {"build_with": ["mcp>=3"]}}`, builtImage)
		putReq := httptest.NewRequest("POST", "/"+workloadName+"/edit", strings.NewReader(body))
		putReq.Header.Set("Content-Type", "application/json")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", workloadName)
		putReq = putReq.WithContext(context.WithValue(putReq.Context(), chi.RouteCtxKey, rctx))

		putW := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(putW, putReq)
		assert.Equal(t, http.StatusBadRequest, putW.Code)
		assert.Contains(t, putW.Body.String(), "runtime_config is only supported for protocol-scheme images")
	})

	t.Run("changed image with echoed runtime_config still 400s", func(t *testing.T) {
		mockWorkloadManager.EXPECT().GetWorkload(gomock.Any(), workloadName).
			Return(core.Workload{Name: workloadName}, nil)
		mockGroupManager.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)

		// Same runtime_config as persisted, but a different image - not an
		// echo of the source, so the guard must still fire.
		body := `{"image": "nginx:latest", "runtime_config": {"builder_image": "python:3.14-slim", ` +
			`"additional_packages": ["ca-certificates"], "build_with": ["mcp<2"], ` +
			`"runtime_env": {"NODE_ENV": "production"}}}`
		putReq := httptest.NewRequest("POST", "/"+workloadName+"/edit", strings.NewReader(body))
		putReq.Header.Set("Content-Type", "application/json")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", workloadName)
		putReq = putReq.WithContext(context.WithValue(putReq.Context(), chi.RouteCtxKey, rctx))

		putW := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(putW, putReq)
		assert.Equal(t, http.StatusBadRequest, putW.Code)
		assert.Contains(t, putW.Body.String(), "runtime_config is only supported for protocol-scheme images")
	})

	t.Run("changed url with echoed runtime_config still 400s", func(t *testing.T) {
		mockWorkloadManager.EXPECT().GetWorkload(gomock.Any(), workloadName).
			Return(core.Workload{Name: workloadName}, nil)
		mockGroupManager.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)

		// Same runtime_config as persisted, but a URL where the persisted
		// workload had none - not an echo of the source.
		body := `{"url": "https://example.com", "runtime_config": {"builder_image": "python:3.14-slim", ` +
			`"additional_packages": ["ca-certificates"], "build_with": ["mcp<2"], ` +
			`"runtime_env": {"NODE_ENV": "production"}}}`
		putReq := httptest.NewRequest("POST", "/"+workloadName+"/edit", strings.NewReader(body))
		putReq.Header.Set("Content-Type", "application/json")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", workloadName)
		putReq = putReq.WithContext(context.WithValue(putReq.Context(), chi.RouteCtxKey, rctx))

		putW := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(putW, putReq)
		assert.Equal(t, http.StatusBadRequest, putW.Code)
		assert.Contains(t, putW.Body.String(), "runtime_config is only supported for protocol-scheme images")
	})
}

// TestUpdateWorkload_RemoteEchoPreservesRuntimeConfig guards against the
// req.URL == "" guard on WithRuntimeConfig silently dropping an accepted
// echo's RuntimeConfig for remote workloads. thv run --remote-url with
// --runtime-image persists a RuntimeConfig on a remote workload today
// (configureRuntimeOptions in cmd/thv/app/run_flags.go does not exclude
// remote workloads), so an unchanged GET-edit-PUT of such a workload must
// round-trip the config, not silently lose it.
//
//nolint:paralleltest // Uses process-wide XDG state settings; keep sequential.
func TestUpdateWorkload_RemoteEchoPreservesRuntimeConfig(t *testing.T) {
	t.Cleanup(xdg.Reload)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	xdg.Reload()

	ctx := context.Background()
	const workloadName = "test-remote-workload"

	persisted := runner.NewRunConfig()
	persisted.Name = workloadName
	persisted.BaseName = workloadName
	persisted.ContainerName = workloadName
	persisted.RemoteURL = "https://mcp.example.com/mcp"
	persisted.RuntimeConfig = &templates.RuntimeConfig{BuilderImage: "python:3.14-slim"}
	require.NoError(t, persisted.SaveState(ctx))

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
	mockRuntime := runtimemocks.NewMockRuntime(ctrl)
	mockGroupManager := groupsmocks.NewMockManager(ctrl)

	routes := &WorkloadRoutes{
		workloadManager:  mockWorkloadManager,
		containerRuntime: mockRuntime,
		groupManager:     mockGroupManager,
		workloadService: &WorkloadService{
			groupManager:      mockGroupManager,
			workloadManager:   mockWorkloadManager,
			configProvider:    config.NewDefaultProvider(),
			imageVerification: retriever.VerifyImageWarn,
		},
	}

	mockWorkloadManager.EXPECT().GetWorkload(gomock.Any(), workloadName).
		Return(core.Workload{Name: workloadName}, nil)

	getReq := httptest.NewRequest("GET", "/"+workloadName, nil)
	getRctx := chi.NewRouteContext()
	getRctx.URLParams.Add("name", workloadName)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), chi.RouteCtxKey, getRctx))
	getW := httptest.NewRecorder()
	apierrors.ErrorHandler(routes.getWorkload).ServeHTTP(getW, getReq)
	require.Equal(t, http.StatusOK, getW.Code, getW.Body.String())
	getBody := getW.Body.Bytes()

	mockWorkloadManager.EXPECT().GetWorkload(gomock.Any(), workloadName).
		Return(core.Workload{Name: workloadName}, nil)
	mockGroupManager.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
	mockWorkloadManager.EXPECT().UpdateWorkload(gomock.Any(), workloadName, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (workloads.CompletionFunc, error) {
			require.NotNil(t, runConfig.RuntimeConfig)
			assert.Equal(t, "python:3.14-slim", runConfig.RuntimeConfig.BuilderImage)
			return nil, nil
		})

	putReq := httptest.NewRequest("POST", "/"+workloadName+"/edit", bytes.NewReader(getBody))
	putReq.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", workloadName)
	putReq = putReq.WithContext(context.WithValue(putReq.Context(), chi.RouteCtxKey, rctx))

	putW := httptest.NewRecorder()
	apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(putW, putReq)
	assert.Equal(t, http.StatusOK, putW.Code, putW.Body.String())
}

// TestUpdateWorkload_PaddedBuilderImageEchoRoundTrips guards normalization
// being applied to the persisted side of the echo comparison, not just the
// request side: a builder_image persisted with surrounding whitespace
// (reachable via --runtime-image, a plain StringVar with no trim-on-store)
// must still compare equal to its own unchanged echo.
//
//nolint:paralleltest // Uses process-wide XDG state settings; keep sequential.
func TestUpdateWorkload_PaddedBuilderImageEchoRoundTrips(t *testing.T) {
	t.Cleanup(xdg.Reload)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	xdg.Reload()

	ctx := context.Background()
	const workloadName = "test-padded-workload"
	builtImage := "toolhivelocal/uvx-arxiv-mcp-server:20260101000000"

	persisted := runner.NewRunConfig()
	persisted.Name = workloadName
	persisted.BaseName = workloadName
	persisted.ContainerName = workloadName
	persisted.Image = builtImage
	persisted.RuntimeConfig = &templates.RuntimeConfig{BuilderImage: "  golang:1.24-alpine  "}
	require.NoError(t, persisted.SaveState(ctx))

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
	mockRuntime := runtimemocks.NewMockRuntime(ctrl)
	mockGroupManager := groupsmocks.NewMockManager(ctrl)

	routes := &WorkloadRoutes{
		workloadManager:  mockWorkloadManager,
		containerRuntime: mockRuntime,
		groupManager:     mockGroupManager,
		workloadService: &WorkloadService{
			groupManager:      mockGroupManager,
			workloadManager:   mockWorkloadManager,
			imageRetriever:    makeMockRetriever(t, builtImage, &regtypes.ImageMetadata{Image: builtImage}, nil),
			imagePuller:       func(_ context.Context, _ string) error { return nil },
			configProvider:    config.NewDefaultProvider(),
			imageVerification: retriever.VerifyImageWarn,
		},
	}

	mockWorkloadManager.EXPECT().GetWorkload(gomock.Any(), workloadName).
		Return(core.Workload{Name: workloadName}, nil)

	getReq := httptest.NewRequest("GET", "/"+workloadName, nil)
	getRctx := chi.NewRouteContext()
	getRctx.URLParams.Add("name", workloadName)
	getReq = getReq.WithContext(context.WithValue(getReq.Context(), chi.RouteCtxKey, getRctx))
	getW := httptest.NewRecorder()
	apierrors.ErrorHandler(routes.getWorkload).ServeHTTP(getW, getReq)
	require.Equal(t, http.StatusOK, getW.Code, getW.Body.String())
	getBody := getW.Body.Bytes()

	mockWorkloadManager.EXPECT().GetWorkload(gomock.Any(), workloadName).
		Return(core.Workload{Name: workloadName}, nil)
	mockGroupManager.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
	mockWorkloadManager.EXPECT().UpdateWorkload(gomock.Any(), workloadName, gomock.Any()).
		Return(nil, nil)

	putReq := httptest.NewRequest("POST", "/"+workloadName+"/edit", bytes.NewReader(getBody))
	putReq.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", workloadName)
	putReq = putReq.WithContext(context.WithValue(putReq.Context(), chi.RouteCtxKey, rctx))

	putW := httptest.NewRecorder()
	apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(putW, putReq)
	assert.Equal(t, http.StatusOK, putW.Code, putW.Body.String())
}

// TestUpdateWorkload_PortReuse tests the port reuse logic when editing workloads
func TestUpdateWorkload_PortReuse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		workloadName   string
		requestBody    string
		existingPort   int
		setupMock      func(*testing.T, *workloadsmocks.MockManager, *runtimemocks.MockRuntime, *groupsmocks.MockManager)
		expectedStatus int
		expectedBody   string
		description    string
	}{
		{
			name:         "Edit with port=0 should reuse existing port",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image", "proxy_port": 0}`,
			existingPort: 8080,
			setupMock: func(t *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				t.Helper()
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload", Port: 8080}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (workloads.CompletionFunc, error) {
						assert.Equal(t, 8080, runConfig.Port, "Port should be reused from existing workload")
						return nil, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
			description:    "When proxy_port is 0, the existing port should be reused",
		},
		{
			name:         "Edit with same port should skip validation",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image", "proxy_port": 8080}`,
			existingPort: 8080,
			setupMock: func(t *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				t.Helper()
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload", Port: 8080}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (workloads.CompletionFunc, error) {
						assert.Equal(t, 8080, runConfig.Port, "Port should remain the same")
						return nil, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
			description:    "When reusing the same port, validation should be skipped",
		},
		{
			name:         "Edit with no port specified should default to existing",
			workloadName: "test-workload",
			requestBody:  `{"image": "test-image"}`,
			existingPort: 8080,
			setupMock: func(t *testing.T, wm *workloadsmocks.MockManager, _ *runtimemocks.MockRuntime, gm *groupsmocks.MockManager) {
				t.Helper()
				wm.EXPECT().GetWorkload(gomock.Any(), "test-workload").
					Return(core.Workload{Name: "test-workload", Port: 8080}, nil)
				gm.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
				wm.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (workloads.CompletionFunc, error) {
						assert.Equal(t, 8080, runConfig.Port, "Port should default to existing port")
						return nil, nil
					})
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "test-workload",
			description:    "When no port is specified in request, existing port should be reused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
			mockRuntime := runtimemocks.NewMockRuntime(ctrl)
			mockGroupManager := groupsmocks.NewMockManager(ctrl)
			tt.setupMock(t, mockWorkloadManager, mockRuntime, mockGroupManager)

			mockRetriever := makeMockRetriever(t,
				"test-image",
				&regtypes.ImageMetadata{Image: "test-image"},
				nil,
			)

			routes := &WorkloadRoutes{
				workloadManager:  mockWorkloadManager,
				containerRuntime: mockRuntime,
				groupManager:     mockGroupManager,
				debugMode:        false,
				workloadService: &WorkloadService{
					groupManager:      mockGroupManager,
					workloadManager:   mockWorkloadManager,
					containerRuntime:  mockRuntime,
					imageRetriever:    mockRetriever,
					imagePuller:       func(_ context.Context, _ string) error { return nil },
					configProvider:    config.NewDefaultProvider(),
					imageVerification: retriever.VerifyImageWarn,
				},
			}

			req := httptest.NewRequest("POST", "/api/v1beta/workloads/"+tt.workloadName+"/edit",
				strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")

			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", tt.workloadName)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code, tt.description)
			assert.Contains(t, w.Body.String(), tt.expectedBody, tt.description)
		})
	}

	// This sub-test must allocate a free port at runtime; it cannot use a
	// hardcoded port number because the port availability check makes a real
	// network bind and an in-use port causes a spurious 400 response.
	t.Run("Edit with explicit port should use that port", func(t *testing.T) {
		t.Parallel()

		// Obtain a free port, then release it so the port-availability check
		// inside config.WithPorts can bind it immediately afterward.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err, "should be able to listen on a free port")
		freePort := ln.Addr().(*net.TCPAddr).Port
		require.NoError(t, ln.Close(), "should be able to release the free port")

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
		mockRuntime := runtimemocks.NewMockRuntime(ctrl)
		mockGroupManager := groupsmocks.NewMockManager(ctrl)

		mockWorkloadManager.EXPECT().GetWorkload(gomock.Any(), "test-workload").
			Return(core.Workload{Name: "test-workload", Port: 8080}, nil)
		mockGroupManager.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)
		mockWorkloadManager.EXPECT().UpdateWorkload(gomock.Any(), "test-workload", gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, runConfig *runner.RunConfig) (workloads.CompletionFunc, error) {
				assert.Equal(t, freePort, runConfig.Port, "Port should be set to explicitly requested port")
				return nil, nil
			})

		mockRetriever := makeMockRetriever(t,
			"test-image",
			&regtypes.ImageMetadata{Image: "test-image"},
			nil,
		)

		routes := &WorkloadRoutes{
			workloadManager:  mockWorkloadManager,
			containerRuntime: mockRuntime,
			groupManager:     mockGroupManager,
			debugMode:        false,
			workloadService: &WorkloadService{
				groupManager:      mockGroupManager,
				workloadManager:   mockWorkloadManager,
				containerRuntime:  mockRuntime,
				imageRetriever:    mockRetriever,
				imagePuller:       func(_ context.Context, _ string) error { return nil },
				configProvider:    config.NewDefaultProvider(),
				imageVerification: retriever.VerifyImageWarn,
			},
		}

		body := fmt.Sprintf(`{"image": "test-image", "proxy_port": %d}`, freePort)
		req := httptest.NewRequest("POST", "/api/v1beta/workloads/test-workload/edit",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", "test-workload")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.updateWorkload).ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code, "When an explicit port is provided, it should be used instead of reusing")
		assert.Contains(t, w.Body.String(), "test-workload", "When an explicit port is provided, it should be used instead of reusing")
	})
}

func makeMockRetriever(
	t *testing.T,
	expectedServerOrImage string,
	returnedServerMetadata regtypes.ServerMetadata,
	expectedRuntimeConfig *templates.RuntimeConfig,
) retriever.Retriever {
	t.Helper()

	return func(_ context.Context, serverOrImage string, _ string, verificationType string, _ string, runtimeConfig *templates.RuntimeConfig) (string, regtypes.ServerMetadata, error) {
		assert.Equal(t, expectedServerOrImage, serverOrImage)
		assert.Equal(t, retriever.VerifyImageWarn, verificationType)
		assert.Equal(t, expectedRuntimeConfig, runtimeConfig)
		return "test-image", returnedServerMetadata, nil
	}
}
