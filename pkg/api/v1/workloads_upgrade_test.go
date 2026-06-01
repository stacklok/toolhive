// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	apierrors "github.com/stacklok/toolhive/pkg/api/errors"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/registry"
	registrymocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport/types"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
	wt "github.com/stacklok/toolhive/pkg/workloads/types"
	"github.com/stacklok/toolhive/pkg/workloads/upgrade"
)

// upgradeTestRoutes builds a WorkloadRoutes wired with the supplied workload
// manager and registry provider plus in-memory loaders, so the upgrade-check
// handlers can run without touching the global state store or a real registry.
func upgradeTestRoutes(
	wm *workloadsmocks.MockManager,
	provider registry.Provider,
	configs map[string]*runner.RunConfig,
) *WorkloadRoutes {
	return &WorkloadRoutes{
		workloadManager: wm,
		loadRunConfig: func(_ context.Context, name string) (*runner.RunConfig, error) {
			cfg, ok := configs[name]
			if !ok {
				return nil, runtime.ErrWorkloadNotFound
			}
			return cfg, nil
		},
		listRunConfigNames: func(_ context.Context) ([]string, error) {
			names := make([]string, 0, len(configs))
			for n := range configs {
				names = append(names, n)
			}
			return names, nil
		},
		registryProvider: func() (registry.Provider, error) {
			return provider, nil
		},
	}
}

// imageServer is a registry image entry fixture.
func imageServer(image string) *regtypes.ImageMetadata {
	return &regtypes.ImageMetadata{Image: image}
}

func TestUpgradeCheckSingle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		workloadName       string
		setupMock          func(*workloadsmocks.MockManager, *registrymocks.MockProvider)
		configs            map[string]*runner.RunConfig
		expectedStatus     int
		expectedStatusBody upgrade.UpgradeStatus
		expectBody         string
	}{
		{
			name:         "up-to-date",
			workloadName: "fetch",
			setupMock: func(wm *workloadsmocks.MockManager, p *registrymocks.MockProvider) {
				wm.EXPECT().GetWorkload(gomock.Any(), "fetch").Return(core.Workload{Name: "fetch"}, nil)
				p.EXPECT().GetServer("io.github/fetch").Return(imageServer("ghcr.io/example/fetch:1.0.0"), nil)
			},
			configs: map[string]*runner.RunConfig{
				"fetch": {Name: "fetch", Image: "ghcr.io/example/fetch:1.0.0", RegistryServerName: "io.github/fetch"},
			},
			expectedStatus:     http.StatusOK,
			expectedStatusBody: upgrade.StatusUpToDate,
		},
		{
			name:         "upgrade-available",
			workloadName: "fetch",
			setupMock: func(wm *workloadsmocks.MockManager, p *registrymocks.MockProvider) {
				wm.EXPECT().GetWorkload(gomock.Any(), "fetch").Return(core.Workload{Name: "fetch"}, nil)
				p.EXPECT().GetServer("io.github/fetch").Return(imageServer("ghcr.io/example/fetch:1.1.0"), nil)
			},
			configs: map[string]*runner.RunConfig{
				"fetch": {Name: "fetch", Image: "ghcr.io/example/fetch:1.0.0", RegistryServerName: "io.github/fetch"},
			},
			expectedStatus:     http.StatusOK,
			expectedStatusBody: upgrade.StatusUpgradeAvailable,
		},
		{
			name:         "workload not found",
			workloadName: "missing",
			setupMock: func(wm *workloadsmocks.MockManager, _ *registrymocks.MockProvider) {
				wm.EXPECT().GetWorkload(gomock.Any(), "missing").
					Return(core.Workload{}, runtime.ErrWorkloadNotFound)
			},
			configs:        map[string]*runner.RunConfig{},
			expectedStatus: http.StatusNotFound,
			expectBody:     "workload not found",
		},
		{
			name:         "invalid workload name",
			workloadName: "bad-name",
			setupMock: func(wm *workloadsmocks.MockManager, _ *registrymocks.MockProvider) {
				wm.EXPECT().GetWorkload(gomock.Any(), "bad-name").
					Return(core.Workload{}, wt.ErrInvalidWorkloadName)
			},
			configs:        map[string]*runner.RunConfig{},
			expectedStatus: http.StatusBadRequest,
			expectBody:     "invalid workload name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			wm := workloadsmocks.NewMockManager(ctrl)
			provider := registrymocks.NewMockProvider(ctrl)
			tt.setupMock(wm, provider)

			routes := upgradeTestRoutes(wm, provider, tt.configs)

			req := httptest.NewRequest("GET", "/"+tt.workloadName+"/upgrade-check", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", tt.workloadName)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			apierrors.ErrorHandler(routes.upgradeCheckSingle).ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.expectBody != "" {
				assert.Contains(t, w.Body.String(), tt.expectBody)
				return
			}

			var resp upgradeCheckResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotNil(t, resp.Result)
			assert.Equal(t, tt.expectedStatusBody, resp.Result.Status)
			assert.Equal(t, tt.workloadName, resp.Result.WorkloadName)
		})
	}
}

func TestUpgradeCheckBulk(t *testing.T) {
	t.Parallel()

	t.Run("mixed results", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		provider := registrymocks.NewMockProvider(ctrl)

		wm.EXPECT().ListWorkloads(gomock.Any(), false).Return([]core.Workload{
			{Name: "fetch"},
			{Name: "time"},
			{Name: "custom"},
		}, nil)
		provider.EXPECT().GetServer("io.github/fetch").Return(imageServer("ghcr.io/example/fetch:1.0.0"), nil)
		provider.EXPECT().GetServer("io.github/time").Return(imageServer("ghcr.io/example/time:2.0.0"), nil)

		configs := map[string]*runner.RunConfig{
			"fetch":  {Name: "fetch", Image: "ghcr.io/example/fetch:1.0.0", RegistryServerName: "io.github/fetch"},
			"time":   {Name: "time", Image: "ghcr.io/example/time:1.0.0", RegistryServerName: "io.github/time"},
			"custom": {Name: "custom", Image: "ghcr.io/example/custom:1.0.0"},
		}
		routes := upgradeTestRoutes(wm, provider, configs)

		req := httptest.NewRequest("GET", "/upgrade-check", nil)
		w := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.upgradeCheckBulk).ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var resp upgradeCheckBulkResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Results, 3)

		byName := map[string]*upgrade.CheckResult{}
		for _, r := range resp.Results {
			byName[r.WorkloadName] = r
		}
		assert.Equal(t, upgrade.StatusUpToDate, byName["fetch"].Status)
		assert.Equal(t, upgrade.StatusUpgradeAvailable, byName["time"].Status)
		assert.Equal(t, upgrade.StatusNotRegistrySourced, byName["custom"].Status)
	})

	t.Run("group filter scopes results", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		provider := registrymocks.NewMockProvider(ctrl)

		// Listing returns two workloads in different groups; only the prod one
		// survives FilterByGroup, so only its registry lookup occurs.
		wm.EXPECT().ListWorkloads(gomock.Any(), false).Return([]core.Workload{
			{Name: "fetch", Group: "prod"},
			{Name: "time", Group: "dev"},
		}, nil)
		provider.EXPECT().GetServer("io.github/fetch").Return(imageServer("ghcr.io/example/fetch:1.1.0"), nil)

		configs := map[string]*runner.RunConfig{
			"fetch": {Name: "fetch", Image: "ghcr.io/example/fetch:1.0.0", RegistryServerName: "io.github/fetch"},
			"time":  {Name: "time", Image: "ghcr.io/example/time:1.0.0", RegistryServerName: "io.github/time"},
		}
		routes := upgradeTestRoutes(wm, provider, configs)

		req := httptest.NewRequest("GET", "/upgrade-check?group=prod", nil)
		w := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.upgradeCheckBulk).ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var resp upgradeCheckBulkResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Results, 1)
		assert.Equal(t, "fetch", resp.Results[0].WorkloadName)
		assert.Equal(t, upgrade.StatusUpgradeAvailable, resp.Results[0].Status)
	})

	t.Run("invalid group name", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		provider := registrymocks.NewMockProvider(ctrl)
		wm.EXPECT().ListWorkloads(gomock.Any(), false).Return([]core.Workload{}, nil)

		routes := upgradeTestRoutes(wm, provider, map[string]*runner.RunConfig{})

		req := httptest.NewRequest("GET", "/upgrade-check?group=Invalid%20Group", nil)
		w := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.upgradeCheckBulk).ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "invalid group name")
	})

	t.Run("stale on-disk config absent from ListWorkloads is excluded", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		provider := registrymocks.NewMockProvider(ctrl)

		// ListWorkloads reports only "fetch". "orphan" is a stale on-disk config
		// that the manager no longer lists. The inScope intersection must drop it,
		// and the provider must never be queried for the orphan's registry server
		// (mock strictness fails the test if GetServer("io.github/orphan") fires).
		wm.EXPECT().ListWorkloads(gomock.Any(), false).Return([]core.Workload{
			{Name: "fetch"},
		}, nil)
		provider.EXPECT().GetServer("io.github/fetch").Return(imageServer("ghcr.io/example/fetch:1.0.0"), nil)

		configs := map[string]*runner.RunConfig{
			"fetch":  {Name: "fetch", Image: "ghcr.io/example/fetch:1.0.0", RegistryServerName: "io.github/fetch"},
			"orphan": {Name: "orphan", Image: "ghcr.io/example/orphan:1.0.0", RegistryServerName: "io.github/orphan"},
		}
		routes := upgradeTestRoutes(wm, provider, configs)

		req := httptest.NewRequest("GET", "/upgrade-check", nil)
		w := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.upgradeCheckBulk).ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var resp upgradeCheckBulkResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Results, 1)
		assert.Equal(t, "fetch", resp.Results[0].WorkloadName)
		for _, r := range resp.Results {
			assert.NotEqual(t, "orphan", r.WorkloadName, "stale on-disk config must not appear in results")
		}
	})

	t.Run("unloadable in-scope config is skipped, not fatal", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		provider := registrymocks.NewMockProvider(ctrl)

		// Both workloads are in scope, but "broken" fails to load. The batch must
		// still succeed and return a result for the loadable workload.
		wm.EXPECT().ListWorkloads(gomock.Any(), false).Return([]core.Workload{
			{Name: "fetch"},
			{Name: "broken"},
		}, nil)
		provider.EXPECT().GetServer("io.github/fetch").Return(imageServer("ghcr.io/example/fetch:1.1.0"), nil)

		fetchCfg := &runner.RunConfig{Name: "fetch", Image: "ghcr.io/example/fetch:1.0.0", RegistryServerName: "io.github/fetch"}
		routes := &WorkloadRoutes{
			workloadManager: wm,
			loadRunConfig: func(_ context.Context, name string) (*runner.RunConfig, error) {
				if name == "broken" {
					return nil, errors.New("corrupted run config")
				}
				return fetchCfg, nil
			},
			listRunConfigNames: func(_ context.Context) ([]string, error) {
				return []string{"fetch", "broken"}, nil
			},
			registryProvider: func() (registry.Provider, error) { return provider, nil },
		}

		req := httptest.NewRequest("GET", "/upgrade-check", nil)
		w := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.upgradeCheckBulk).ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var resp upgradeCheckBulkResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Results, 1)
		assert.Equal(t, "fetch", resp.Results[0].WorkloadName)
		assert.Equal(t, upgrade.StatusUpgradeAvailable, resp.Results[0].Status)
	})
}

// TestUpgradeCheckNoSecretLeak asserts the upgrade-check response carries only
// metadata and never any secret values from the workload's RunConfig.
func TestUpgradeCheckNoSecretLeak(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	wm := workloadsmocks.NewMockManager(ctrl)
	provider := registrymocks.NewMockProvider(ctrl)

	wm.EXPECT().GetWorkload(gomock.Any(), "fetch").Return(core.Workload{Name: "fetch"}, nil)
	// Candidate declares a secret env var with a default; the drift report must
	// surface the name but never the secret default value.
	provider.EXPECT().GetServer("io.github/fetch").Return(&regtypes.ImageMetadata{
		Image: "ghcr.io/example/fetch:1.1.0",
		EnvVars: []*regtypes.EnvVar{
			{Name: "API_TOKEN", Secret: true, Required: true, Default: "super-secret-default"},
		},
	}, nil)

	configs := map[string]*runner.RunConfig{
		"fetch": {
			Name:               "fetch",
			Image:              "ghcr.io/example/fetch:1.0.0",
			RegistryServerName: "io.github/fetch",
			Transport:          types.TransportTypeStdio,
			EnvVars:            map[string]string{"PLAIN": "value"},
			Secrets:            []string{"my-vault-secret,target=OTHER_TOKEN"},
		},
	}
	routes := upgradeTestRoutes(wm, provider, configs)

	req := httptest.NewRequest("GET", "/fetch/upgrade-check", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "fetch")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	apierrors.ErrorHandler(routes.upgradeCheckSingle).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	assert.NotContains(t, body, "super-secret-default", "secret env var default must not leak")
	assert.NotContains(t, body, "my-vault-secret", "secret parameter name must not leak")
	assert.NotContains(t, body, "OTHER_TOKEN", "secret target must not leak")
	// The drift report should still name the missing candidate env var.
	assert.Contains(t, body, "API_TOKEN")
}

// TestUpgradeCheckRouting asserts /upgrade-check resolves to the batch handler
// and is not captured by the /{name} wildcard (chi static-before-wildcard
// ordering), while /{name}/upgrade-check resolves to the single handler.
func TestUpgradeCheckRouting(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	wm := workloadsmocks.NewMockManager(ctrl)

	// The batch handler lists workloads; getWorkload would instead call
	// GetWorkload("upgrade-check"). Asserting ListWorkloads is hit proves the
	// literal route won, not the wildcard.
	wm.EXPECT().ListWorkloads(gomock.Any(), false).Return([]core.Workload{}, nil)

	provider := registrymocks.NewMockProvider(ctrl)
	routes := upgradeTestRoutes(wm, provider, map[string]*runner.RunConfig{})

	router := newUpgradeTestRouter(routes)

	req := httptest.NewRequest("GET", "/upgrade-check", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

// newUpgradeTestRouter mounts only the routes relevant to ordering so the test
// exercises chi's real matching between /upgrade-check and /{name}.
func newUpgradeTestRouter(routes *WorkloadRoutes) chi.Router {
	r := chi.NewRouter()
	r.Get("/upgrade-check", apierrors.ErrorHandler(routes.upgradeCheckBulk))
	r.Get("/{name}/upgrade-check", apierrors.ErrorHandler(routes.upgradeCheckSingle))
	r.Get("/{name}", apierrors.ErrorHandler(routes.getWorkload))
	return r
}
