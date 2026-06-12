// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	apierrors "github.com/stacklok/toolhive/pkg/api/errors"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	groupsmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
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

		gm := groupsmocks.NewMockManager(ctrl)
		gm.EXPECT().Exists(gomock.Any(), "prod").Return(true, nil)

		configs := map[string]*runner.RunConfig{
			"fetch": {Name: "fetch", Image: "ghcr.io/example/fetch:1.0.0", RegistryServerName: "io.github/fetch"},
			"time":  {Name: "time", Image: "ghcr.io/example/time:1.0.0", RegistryServerName: "io.github/time"},
		}
		routes := upgradeTestRoutes(wm, provider, configs)
		routes.groupManager = gm

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

	t.Run("unknown group returns 404", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		provider := registrymocks.NewMockProvider(ctrl)

		// The group does not exist, so no per-workload registry lookup happens.
		wm.EXPECT().ListWorkloads(gomock.Any(), false).Return([]core.Workload{
			{Name: "fetch", Group: "prod"},
		}, nil)

		gm := groupsmocks.NewMockManager(ctrl)
		gm.EXPECT().Exists(gomock.Any(), "ghost").Return(false, nil)

		routes := upgradeTestRoutes(wm, provider, map[string]*runner.RunConfig{})
		routes.groupManager = gm

		req := httptest.NewRequest("GET", "/upgrade-check?group=ghost", nil)
		w := httptest.NewRecorder()
		apierrors.ErrorHandler(routes.upgradeCheckBulk).ServeHTTP(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
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

// stubApplier is a workloadUpgradeApplier test double. It records the arguments
// it was called with and returns the configured result/error.
type stubApplier struct {
	called  bool
	gotName string
	gotOpts upgrade.ApplyOptions
	result  *upgrade.CheckResult
	err     error
	// errFromOpts, when set, derives the returned error from the received opts.
	// It lets a test feed a secret reference in via the request body and confirm
	// the handler does not propagate it to the response even when the underlying
	// error mentions it.
	errFromOpts func(upgrade.ApplyOptions) error
}

func (s *stubApplier) Apply(_ context.Context, name string, opts upgrade.ApplyOptions) (*upgrade.CheckResult, error) {
	s.called = true
	s.gotName = name
	s.gotOpts = opts
	if s.errFromOpts != nil {
		return nil, s.errFromOpts(opts)
	}
	return s.result, s.err
}

// upgradeApplyRoutes wires a WorkloadRoutes for the POST upgrade handler with a
// stub applier and a workloadService carrying the verification setting the
// handler reads.
func upgradeApplyRoutes(wm *workloadsmocks.MockManager, applier workloadUpgradeApplier) *WorkloadRoutes {
	return &WorkloadRoutes{
		workloadManager: wm,
		workloadService: &WorkloadService{imageVerification: "warn"},
		applierFactory: func() (workloadUpgradeApplier, error) {
			return applier, nil
		},
	}
}

// doUpgradeRequest issues a POST /{name}/upgrade against the handler with the
// chi route context populated, returning the recorder.
func doUpgradeRequest(routes *WorkloadRoutes, name, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/"+name+"/upgrade", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", name)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	apierrors.ErrorHandler(routes.upgradeWorkload).ServeHTTP(w, req)
	return w
}

func TestUpgradeWorkload(t *testing.T) {
	t.Parallel()

	t.Run("happy path applies upgrade and returns applied result", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		wm.EXPECT().GetWorkload(gomock.Any(), "fetch").Return(core.Workload{Name: "fetch"}, nil)

		applier := &stubApplier{
			result: &upgrade.CheckResult{
				WorkloadName:   "fetch",
				Status:         upgrade.StatusUpgradeAvailable,
				CurrentImage:   "ghcr.io/example/fetch:1.0.0",
				CandidateImage: "ghcr.io/example/fetch:1.1.0",
			},
		}
		routes := upgradeApplyRoutes(wm, applier)

		w := doUpgradeRequest(routes, "fetch", `{"env":{"FOO":"bar"},"secrets":["vault,target=TOKEN"]}`)

		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, applier.called, "applier must be invoked")
		assert.Equal(t, "fetch", applier.gotName)
		// The handler must pass through the request env/secrets and pin the
		// non-interactive validator + verification setting.
		assert.Equal(t, map[string]string{"FOO": "bar"}, applier.gotOpts.EnvVars)
		assert.Equal(t, []string{"vault,target=TOKEN"}, applier.gotOpts.Secrets)
		assert.IsType(t, &runner.DetachedEnvVarValidator{}, applier.gotOpts.EnvVarValidator)
		assert.Equal(t, "warn", applier.gotOpts.VerifySetting)

		var resp upgradeCheckResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp.Result)
		assert.Equal(t, upgrade.StatusUpgradeAvailable, resp.Result.Status)
		assert.Equal(t, "ghcr.io/example/fetch:1.1.0", resp.Result.CandidateImage)
		// The success response is built solely from the CheckResult; the secret
		// reference supplied in the request body must never appear in it.
		assert.NotContains(t, w.Body.String(), "vault", "secret reference must not leak into success response")
	})

	t.Run("empty body is tolerated", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		wm.EXPECT().GetWorkload(gomock.Any(), "fetch").Return(core.Workload{Name: "fetch"}, nil)

		applier := &stubApplier{
			result: &upgrade.CheckResult{WorkloadName: "fetch", Status: upgrade.StatusUpgradeAvailable},
		}
		routes := upgradeApplyRoutes(wm, applier)

		w := doUpgradeRequest(routes, "fetch", "")

		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, applier.called)
		assert.Nil(t, applier.gotOpts.EnvVars)
		assert.Nil(t, applier.gotOpts.Secrets)
	})

	t.Run("no-op when not upgrade-available returns result without recreating", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		wm.EXPECT().GetWorkload(gomock.Any(), "fetch").Return(core.Workload{Name: "fetch"}, nil)
		// Strict mock: UpdateWorkload must never be called on a no-op. The stub
		// applier returns up-to-date without touching the manager, so any
		// UpdateWorkload expectation is intentionally absent and would fail.

		applier := &stubApplier{
			result: &upgrade.CheckResult{WorkloadName: "fetch", Status: upgrade.StatusUpToDate},
		}
		routes := upgradeApplyRoutes(wm, applier)

		w := doUpgradeRequest(routes, "fetch", "")

		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, applier.called)

		var resp upgradeCheckResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp.Result)
		assert.Equal(t, upgrade.StatusUpToDate, resp.Result.Status)
	})

	t.Run("apply failure maps to 422 without leaking secrets", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		wm.EXPECT().GetWorkload(gomock.Any(), "fetch").Return(core.Workload{Name: "fetch"}, nil)

		// Worst case for leakage: the applier's own error embeds the secret
		// reference it received from the request body. The handler must still
		// produce a 422 whose response body does NOT carry that secret string,
		// proving it does not propagate request-derived secret data to clients.
		applier := &stubApplier{
			errFromOpts: func(opts upgrade.ApplyOptions) error {
				return fmt.Errorf("image verification failed processing %q", opts.Secrets[0])
			},
		}
		routes := upgradeApplyRoutes(wm, applier)

		w := doUpgradeRequest(routes, "fetch", `{"secrets":["super-secret-value,target=TOKEN"]}`)

		require.Equal(t, http.StatusUnprocessableEntity, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "failed to apply upgrade")
		assert.NotContains(t, body, "super-secret-value", "secret reference must not leak into error response")
	})

	t.Run("post-destruction failure maps to 500", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		wm.EXPECT().GetWorkload(gomock.Any(), "fetch").Return(core.Workload{Name: "fetch"}, nil)

		// A failure tagged ErrApplyAfterDestroy means the workload was already torn
		// down: the handler must report 5xx (state uncertain), not 422 (which would
		// imply the workload is intact and the request is safe to retry).
		applier := &stubApplier{
			err: fmt.Errorf("completion failed: %w", upgrade.ErrApplyAfterDestroy),
		}
		routes := upgradeApplyRoutes(wm, applier)

		w := doUpgradeRequest(routes, "fetch", "")

		// 500, not 422. httperr sanitizes 5xx bodies to a generic message, so the
		// detailed cause stays server-side (asserted by absence below).
		require.Equal(t, http.StatusInternalServerError, w.Code)
		assert.NotContains(t, w.Body.String(), "uncertain", "5xx body must not echo internal detail")
	})

	t.Run("unknown workload returns 404", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		wm.EXPECT().GetWorkload(gomock.Any(), "missing").
			Return(core.Workload{}, runtime.ErrWorkloadNotFound)

		applier := &stubApplier{}
		routes := upgradeApplyRoutes(wm, applier)

		w := doUpgradeRequest(routes, "missing", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.False(t, applier.called, "applier must not run for an unknown workload")
	})

	t.Run("invalid workload name returns 400", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		wm.EXPECT().GetWorkload(gomock.Any(), "bad-name").
			Return(core.Workload{}, wt.ErrInvalidWorkloadName)

		applier := &stubApplier{}
		routes := upgradeApplyRoutes(wm, applier)

		w := doUpgradeRequest(routes, "bad-name", "")

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.False(t, applier.called)
	})
}

// TestUpgradeCheckRouting asserts /upgrade-check resolves to the batch handler
// and is not captured by the /{name} wildcard (chi static-before-wildcard
// ordering), while /{name}/upgrade-check resolves to the single handler and
// POST /{name}/upgrade resolves to the apply handler (not getWorkload).
func TestUpgradeCheckRouting(t *testing.T) {
	t.Parallel()

	t.Run("GET /upgrade-check resolves to batch handler", func(t *testing.T) {
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

		req := httptest.NewRequest("GET", "/upgrade-check", nil)
		w := httptest.NewRecorder()
		newUpgradeTestRouter(routes).ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("POST /{name}/upgrade resolves to apply handler, not getWorkload", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		wm := workloadsmocks.NewMockManager(ctrl)
		// getWorkload never matches a POST. To prove the apply handler ran (and not
		// some wildcard fallthrough), the apply handler's GetWorkload existence
		// check fires and the injected applier records the call.
		wm.EXPECT().GetWorkload(gomock.Any(), "fetch").Return(core.Workload{Name: "fetch"}, nil)

		applier := &stubApplier{
			result: &upgrade.CheckResult{WorkloadName: "fetch", Status: upgrade.StatusUpToDate},
		}
		provider := registrymocks.NewMockProvider(ctrl)
		routes := upgradeTestRoutes(wm, provider, map[string]*runner.RunConfig{})
		routes.workloadService = &WorkloadService{imageVerification: "warn"}
		routes.applierFactory = func() (workloadUpgradeApplier, error) { return applier, nil }

		req := httptest.NewRequest("POST", "/fetch/upgrade", strings.NewReader(""))
		w := httptest.NewRecorder()
		newUpgradeTestRouter(routes).ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.True(t, applier.called, "POST /{name}/upgrade must resolve to the apply handler")
	})
}

// newUpgradeTestRouter mounts only the routes relevant to ordering so the test
// exercises chi's real matching between the literal upgrade routes and the
// /{name} wildcard.
func newUpgradeTestRouter(routes *WorkloadRoutes) chi.Router {
	r := chi.NewRouter()
	r.Get("/upgrade-check", apierrors.ErrorHandler(routes.upgradeCheckBulk))
	r.Get("/{name}/upgrade-check", apierrors.ErrorHandler(routes.upgradeCheckSingle))
	r.Post("/{name}/upgrade", apierrors.ErrorHandler(routes.upgradeWorkload))
	r.Get("/{name}", apierrors.ErrorHandler(routes.getWorkload))
	return r
}
