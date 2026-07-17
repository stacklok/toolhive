// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/permissions"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/auth"
	appconfig "github.com/stacklok/toolhive/pkg/config"
	configmocks "github.com/stacklok/toolhive/pkg/config/mocks"
	"github.com/stacklok/toolhive/pkg/container/templates"
	registrymocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/telemetry"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
	workloadmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

const applyServerName = "example-server"

// applierHarness wires an Applier with mocked dependencies and swappable
// resolve/enforce/load function fields for deterministic, disk-free tests.
type applierHarness struct {
	applier    *Applier
	manager    *workloadmocks.MockManager
	provider   *registrymocks.MockProvider
	configMock *configmocks.MockProvider

	// loadState is what loadStateFn returns.
	loadConfig *runner.RunConfig
	loadErr    error

	// resolve controls resolveFn.
	resolveImageURL string
	resolveMeta     regtypes.ServerMetadata
	resolveErr      error

	// enforce controls enforcePullFn.
	enforceErr error

	// captured records ordering and the config passed to each stage.
	calls          []string
	enforcedConfig *runner.RunConfig
	updatedConfig  *runner.RunConfig
}

func newApplierHarness(t *testing.T) *applierHarness {
	t.Helper()
	ctrl := gomock.NewController(t)

	h := &applierHarness{
		manager:    workloadmocks.NewMockManager(ctrl),
		provider:   registrymocks.NewMockProvider(ctrl),
		configMock: configmocks.NewMockProvider(ctrl),
	}

	checker, err := NewChecker(h.provider)
	require.NoError(t, err)

	applier, err := NewApplier(h.manager, checker, h.configMock)
	require.NoError(t, err)

	// Swap the package-level function wrappers for stubs that record ordering.
	applier.loadStateFn = func(_ context.Context, _ string) (*runner.RunConfig, error) {
		h.calls = append(h.calls, "load")
		return h.loadConfig, h.loadErr
	}
	applier.resolveFn = func(
		_ context.Context, _ string, _ string, _ string, _ string, _ *templates.RuntimeConfig,
	) (string, regtypes.ServerMetadata, error) {
		h.calls = append(h.calls, "resolve")
		return h.resolveImageURL, h.resolveMeta, h.resolveErr
	}
	applier.enforcePullFn = func(
		_ context.Context, rc *runner.RunConfig, _ regtypes.ServerMetadata, _ string,
		_ retriever.ImagePuller, _ time.Duration, _ bool,
	) error {
		h.calls = append(h.calls, "enforce")
		h.enforcedConfig = rc
		return h.enforceErr
	}

	h.applier = applier
	return h
}

// expectUpdate sets up the UpdateWorkload mock to record the config and order.
func (h *applierHarness) expectUpdate() {
	h.manager.EXPECT().
		UpdateWorkload(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, cfg *runner.RunConfig) (workloads.CompletionFunc, error) {
			h.calls = append(h.calls, "update")
			h.updatedConfig = cfg
			// The completion func records that Apply awaited it. UpdateWorkload
			// persists the new config asynchronously, so Apply must block on this
			// before returning or callers observe a stale, pre-upgrade workload.
			return func() error { h.calls = append(h.calls, "complete"); return nil }, nil
		})
}

func upgradeableConfig(t *testing.T) *runner.RunConfig {
	t.Helper()
	// Use a real directory for the volume source: the run config builder
	// validates volume source paths against the filesystem.
	volSrc := t.TempDir()
	return &runner.RunConfig{
		Name:               "wl",
		Image:              "ghcr.io/example/server:1.0.0",
		RegistryServerName: applyServerName,
		Group:              "default",
		Transport:          transporttypes.TransportTypeStdio,
		ProxyMode:          transporttypes.ProxyModeStreamableHTTP,
		Port:               8080,
		EnvVars:            map[string]string{"LOG_LEVEL": "info"},
		Secrets:            []string{"mykey,target=API_KEY"},
		Volumes:            []string{volSrc + ":/container"},
	}
}

func TestNewApplier(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mgr := workloadmocks.NewMockManager(ctrl)
	checker, err := NewChecker(registrymocks.NewMockProvider(ctrl))
	require.NoError(t, err)
	cfgProvider := configmocks.NewMockProvider(ctrl)

	t.Run("nil manager fails", func(t *testing.T) {
		t.Parallel()
		a, err := NewApplier(nil, checker, cfgProvider)
		require.Error(t, err)
		assert.Nil(t, a)
	})

	t.Run("nil checker fails", func(t *testing.T) {
		t.Parallel()
		a, err := NewApplier(mgr, nil, cfgProvider)
		require.Error(t, err)
		assert.Nil(t, a)
	})

	t.Run("nil config provider fails", func(t *testing.T) {
		t.Parallel()
		a, err := NewApplier(mgr, checker, nil)
		require.Error(t, err)
		assert.Nil(t, a)
	})

	t.Run("valid inputs succeed with real funcs populated", func(t *testing.T) {
		t.Parallel()
		a, err := NewApplier(mgr, checker, cfgProvider)
		require.NoError(t, err)
		require.NotNil(t, a)
		assert.NotNil(t, a.resolveFn)
		assert.NotNil(t, a.enforcePullFn)
		assert.NotNil(t, a.loadStateFn)
	})
}

func TestApplier_Apply_NoOpWhenNotUpgradeAvailable(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	// up-to-date: candidate equals current => StatusUpToDate.
	h.loadConfig = upgradeableConfig(t)
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.0.0"}, nil)

	// UpdateWorkload, resolve, enforce must NEVER be called.
	res, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, StatusUpToDate, res.Status)
	assert.NotContains(t, h.calls, "resolve")
	assert.NotContains(t, h.calls, "enforce")
	assert.NotContains(t, h.calls, "update")
}

func TestApplier_Apply_NoOpWhenNotRegistrySourced(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	cfg := upgradeableConfig(t)
	cfg.RegistryServerName = "" // not registry-sourced
	h.loadConfig = cfg

	res, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, StatusNotRegistrySourced, res.Status)
	assert.NotContains(t, h.calls, "resolve")
	assert.NotContains(t, h.calls, "update")
}

func TestApplier_Apply_LoadStateFailure(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)
	h.loadErr = errors.New("no such workload")

	res, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.NotContains(t, h.calls, "update")
}

func TestApplier_Apply_ResolveFailureLeavesWorkloadUntouched(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	h.loadConfig = upgradeableConfig(t)
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	h.resolveErr = errors.New("verification failed")

	res, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{})
	require.Error(t, err)
	assert.Nil(t, res)
	// resolve attempted, but no enforce and no destructive update.
	assert.Contains(t, h.calls, "resolve")
	assert.NotContains(t, h.calls, "enforce")
	assert.NotContains(t, h.calls, "update")
}

func TestApplier_Apply_RemoteMetadataIsRejected(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	h.loadConfig = upgradeableConfig(t)
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	// Resolve succeeds but returns a non-image (remote) metadata.
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = &regtypes.RemoteServerMetadata{}

	res, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, h.calls, "resolve")
	assert.NotContains(t, h.calls, "enforce")
	assert.NotContains(t, h.calls, "update")
}

func TestApplier_Apply_EnforcePullFailureLeavesWorkloadUntouched(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	h.loadConfig = upgradeableConfig(t)
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = &regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}
	h.configMock.EXPECT().GetConfig().Return(&appconfig.Config{}).AnyTimes()
	h.enforceErr = errors.New("policy denied")

	res, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
	})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, h.calls, "enforce")
	assert.NotContains(t, h.calls, "update")
}

func TestApplier_Apply_HappyPath(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	old := upgradeableConfig(t)
	h.loadConfig = old
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	candidate := &regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}
	candidate.Name = applyServerName
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = candidate
	h.configMock.EXPECT().
		GetConfig().
		Return(&appconfig.Config{RegistryApiUrl: "https://api.example", RegistryUrl: "https://reg.example"}).
		AnyTimes()
	h.expectUpdate()

	res, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{
		EnvVars:         map[string]string{"EXTRA": "val"},
		Secrets:         []string{"othersecret,target=OTHER"},
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, StatusUpgradeAvailable, res.Status)

	// The destructive update must have run with the candidate image.
	require.NotNil(t, h.updatedConfig)
	assert.Equal(t, "ghcr.io/example/server:1.2.0", h.updatedConfig.Image, "image must be the candidate")
	assert.Equal(t, applyServerName, h.updatedConfig.RegistryServerName, "registry server name preserved")
	assert.Equal(t, "wl", h.updatedConfig.Name, "name preserved")
	assert.Equal(t, "default", h.updatedConfig.Group, "group preserved")

	// User env vars preserved + opts merged.
	assert.Equal(t, "info", h.updatedConfig.EnvVars["LOG_LEVEL"], "existing env preserved")
	assert.Equal(t, "val", h.updatedConfig.EnvVars["EXTRA"], "opts env merged")

	// Secrets preserved + opts merged.
	assert.Contains(t, h.updatedConfig.Secrets, "mykey,target=API_KEY", "existing secret preserved")
	assert.Contains(t, h.updatedConfig.Secrets, "othersecret,target=OTHER", "opts secret merged")

	// Registry source URLs recorded from app config.
	assert.Equal(t, "https://api.example", h.updatedConfig.RegistryAPIURL)
	assert.Equal(t, "https://reg.example", h.updatedConfig.RegistryURL)

	// Security guarantee: the exact config that was gated+pulled is the one that
	// gets deployed, so the verified image is the one that runs.
	assert.Same(t, h.enforcedConfig, h.updatedConfig, "pulled config must be the deployed config")
}

func TestApplier_Apply_OptsEnvOverridesExisting(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	h.loadConfig = upgradeableConfig(t)
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = &regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}
	h.configMock.EXPECT().GetConfig().Return(&appconfig.Config{}).AnyTimes()
	h.expectUpdate()

	_, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{
		EnvVars:         map[string]string{"LOG_LEVEL": "debug"}, // overrides existing "info"
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
	})
	require.NoError(t, err)
	require.NotNil(t, h.updatedConfig)
	assert.Equal(t, "debug", h.updatedConfig.EnvVars["LOG_LEVEL"], "opts env overrides existing")
}

func TestApplier_Apply_DoesNotMutateOldConfig(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	old := upgradeableConfig(t)
	// A non-named profile object plus a volume is what exposes the mutation: the
	// builder's processVolumeMounts appends the volume to PermissionProfile.Read
	// or .Write. If the builder is handed old's profile by pointer, those appends
	// leak back into old. (upgradeableConfig already sets a Volume.)
	old.PermissionProfile = &permissions.Profile{Name: "custom"}
	wantVolumes := slices.Clone(old.Volumes)
	wantProfileRead := slices.Clone(old.PermissionProfile.Read)
	wantProfileWrite := slices.Clone(old.PermissionProfile.Write)
	wantOverride := map[string]runner.ToolOverride{"a": {Name: "b"}}
	old.ToolsOverride = map[string]runner.ToolOverride{"a": {Name: "b"}}
	h.loadConfig = old
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = &regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}
	h.configMock.EXPECT().GetConfig().Return(&appconfig.Config{}).AnyTimes()
	h.expectUpdate()

	_, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{
		EnvVars:         map[string]string{"EXTRA": "val"},
		Secrets:         []string{"othersecret,target=OTHER"},
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
	})
	require.NoError(t, err)

	// old must be untouched: image, env map, secrets slice, volumes slice.
	assert.Equal(t, "ghcr.io/example/server:1.0.0", old.Image, "old image unchanged")
	assert.Equal(t, map[string]string{"LOG_LEVEL": "info"}, old.EnvVars, "old EnvVars unchanged")
	assert.Equal(t, []string{"mykey,target=API_KEY"}, old.Secrets, "old Secrets unchanged")
	assert.Equal(t, wantVolumes, old.Volumes, "old Volumes unchanged")
	// old's permission profile must not gain mounts from the volume processing.
	assert.Equal(t, wantProfileRead, old.PermissionProfile.Read, "old profile Read unchanged")
	assert.Equal(t, wantProfileWrite, old.PermissionProfile.Write, "old profile Write unchanged")
	assert.Equal(t, wantOverride, old.ToolsOverride, "old ToolsOverride unchanged")
}

func TestApplier_Apply_VerifyAndPullHappenBeforeUpdate(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	h.loadConfig = upgradeableConfig(t)
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = &regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}
	h.configMock.EXPECT().GetConfig().Return(&appconfig.Config{}).AnyTimes()
	h.expectUpdate()

	_, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
	})
	require.NoError(t, err)

	// Ordering: load -> resolve -> enforce(verify+pull) -> update -> await completion.
	assert.Equal(t, []string{"load", "resolve", "enforce", "update", "complete"}, h.calls)
}

func TestApplier_Apply_CompletionFailureSurfaces(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	h.loadConfig = upgradeableConfig(t)
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = &regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}
	h.configMock.EXPECT().GetConfig().Return(&appconfig.Config{}).AnyTimes()

	// UpdateWorkload starts the recreate but its completion reports a failure
	// (e.g. the new workload failed to start). Apply must await completion and
	// surface that error rather than reporting a successful upgrade.
	h.manager.EXPECT().
		UpdateWorkload(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ *runner.RunConfig) (workloads.CompletionFunc, error) {
			h.calls = append(h.calls, "update")
			return func() error { return errors.New("new workload failed to start") }, nil
		})

	_, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to complete upgrade")
	// The error MUST carry ErrApplyAfterDestroy: the recreate already began, so
	// callers (e.g. the API handler) map this to 5xx, not a 422 "nothing changed".
	assert.ErrorIs(t, err, ErrApplyAfterDestroy)
	assert.Contains(t, h.calls, "update")
}

func TestApplier_Apply_UpdateFailureIsTaggedAfterDestroy(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	h.loadConfig = upgradeableConfig(t)
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = &regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}
	h.configMock.EXPECT().GetConfig().Return(&appconfig.Config{}).AnyTimes()

	// UpdateWorkload itself fails after initiating the stop/delete: the workload
	// state is uncertain, so the error must carry ErrApplyAfterDestroy.
	h.manager.EXPECT().
		UpdateWorkload(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ *runner.RunConfig) (workloads.CompletionFunc, error) {
			h.calls = append(h.calls, "update")
			return nil, errors.New("stop/delete failed mid-recreate")
		})

	_, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update workload")
	assert.ErrorIs(t, err, ErrApplyAfterDestroy)
}

// fullyConfiguredOld builds a workload config exercising the breadth of
// user-owned RunConfig fields, including HTTP transport with explicit ports and
// a named permission profile, to guard against the config-loss regression.
func fullyConfiguredOld(t *testing.T) *runner.RunConfig {
	t.Helper()
	volSrc := t.TempDir()

	mw, err := transporttypes.NewMiddlewareConfig(auth.MiddlewareType, auth.MiddlewareParams{})
	require.NoError(t, err)

	return &runner.RunConfig{
		Name:                        "wl",
		Image:                       "ghcr.io/example/server:1.0.0",
		RegistryServerName:          applyServerName,
		Group:                       "prod",
		Transport:                   transporttypes.TransportTypeStreamableHTTP,
		ProxyMode:                   transporttypes.ProxyModeStreamableHTTP,
		Host:                        "127.0.0.1",
		Port:                        18080,
		TargetPort:                  19090,
		TargetHost:                  "10.0.0.5",
		Publish:                     []string{"18080:18080"},
		EnvVars:                     map[string]string{"LOG_LEVEL": "info"},
		Secrets:                     []string{"mykey,target=API_KEY"},
		Volumes:                     []string{volSrc + ":/container"},
		CmdArgs:                     []string{"--flag", "value"},
		PermissionProfileNameOrPath: "none",
		IsolateNetwork:              true,
		TrustProxyHeaders:           true,
		Stateless:                   true,
		EndpointPrefix:              "/mcp",
		SessionTTL:                  "30m",
		Debug:                       true,
		ContainerLabels:             map[string]string{"team": "platform"},
		OIDCConfig:                  &auth.TokenValidatorConfig{Issuer: "https://issuer.example", Audience: "aud"},
		AuthzConfigPath:             "/etc/thv/authz.yaml",
		AuditConfigPath:             "/etc/thv/audit.yaml",
		TelemetryConfig:             &telemetry.Config{Endpoint: "otel.example:4317", ServiceName: "wl"},
		ToolsFilter:                 []string{"safe_tool"},
		ToolsOverride:               map[string]runner.ToolOverride{"a": {Name: "b"}},
		MiddlewareConfigs:           []transporttypes.MiddlewareConfig{*mw},
	}
}

func TestApplier_Apply_PreservesFullUserConfig(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	old := fullyConfiguredOld(t)
	h.loadConfig = old
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	// The candidate metadata advertises the same HTTP transport so the builder
	// does not need to invent transport/ports; ports come from old.
	candidate := &regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}
	candidate.Transport = string(transporttypes.TransportTypeStreamableHTTP)
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = candidate
	h.configMock.EXPECT().
		GetConfig().
		Return(&appconfig.Config{RegistryApiUrl: "https://api.example", RegistryUrl: "https://reg.example"}).
		AnyTimes()
	h.expectUpdate()

	_, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
	})
	require.NoError(t, err)
	require.NotNil(t, h.updatedConfig)
	got := h.updatedConfig

	// The ONLY changes are image and registry URLs (no env/secret opts here).
	assert.Equal(t, "ghcr.io/example/server:1.2.0", got.Image, "image is candidate")
	assert.Equal(t, "https://api.example", got.RegistryAPIURL)
	assert.Equal(t, "https://reg.example", got.RegistryURL)

	// Everything else preserved from old (the config-loss regression guard).
	assert.Equal(t, old.Name, got.Name)
	assert.Equal(t, old.Group, got.Group)
	assert.Equal(t, old.Transport, got.Transport, "HTTP transport preserved")
	assert.Equal(t, old.Host, got.Host)
	assert.Equal(t, old.Port, got.Port, "HTTP proxy port preserved")
	assert.Equal(t, old.TargetPort, got.TargetPort, "HTTP target port preserved")
	assert.Equal(t, "10.0.0.5", got.TargetHost, "target host preserved (not re-derived)")
	assert.Equal(t, old.Publish, got.Publish, "published ports preserved")
	assert.Equal(t, old.ProxyMode, got.ProxyMode)
	assert.Equal(t, old.CmdArgs, got.CmdArgs)
	assert.Equal(t, old.PermissionProfileNameOrPath, got.PermissionProfileNameOrPath, "named profile preserved")
	assert.Equal(t, old.IsolateNetwork, got.IsolateNetwork)
	assert.Equal(t, old.TrustProxyHeaders, got.TrustProxyHeaders)
	assert.Equal(t, old.Stateless, got.Stateless)
	assert.Equal(t, old.EndpointPrefix, got.EndpointPrefix)
	assert.Equal(t, old.SessionTTL, got.SessionTTL)
	assert.Equal(t, old.Debug, got.Debug)
	assert.Equal(t, "platform", got.ContainerLabels["team"], "user label preserved")
	assert.Equal(t, old.OIDCConfig, got.OIDCConfig, "OIDC config preserved")
	assert.Equal(t, old.AuthzConfigPath, got.AuthzConfigPath, "authz path preserved")
	assert.Equal(t, old.AuditConfigPath, got.AuditConfigPath, "audit path preserved")
	assert.Equal(t, old.TelemetryConfig, got.TelemetryConfig, "telemetry preserved")
	assert.Equal(t, old.ToolsFilter, got.ToolsFilter, "tools filter preserved (security)")
	assert.Equal(t, old.ToolsOverride, got.ToolsOverride)
	assert.Equal(t, old.MiddlewareConfigs, got.MiddlewareConfigs, "middleware chain preserved")
}

func TestApplier_Apply_PreservesHTTPTransportPorts(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	old := &runner.RunConfig{
		Name:               "wl",
		Image:              "ghcr.io/example/server:1.0.0",
		RegistryServerName: applyServerName,
		Group:              "default",
		Transport:          transporttypes.TransportTypeStreamableHTTP,
		ProxyMode:          transporttypes.ProxyModeStreamableHTTP,
		Port:               21000,
		TargetPort:         22000,
		EnvVars:            map[string]string{},
	}
	h.loadConfig = old
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	candidate := &regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}
	candidate.Transport = string(transporttypes.TransportTypeStreamableHTTP)
	// Candidate advertises DIFFERENT registry ports; old's ports must win.
	candidate.ProxyPort = 30000
	candidate.TargetPort = 31000
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = candidate
	h.configMock.EXPECT().GetConfig().Return(&appconfig.Config{}).AnyTimes()
	h.expectUpdate()

	_, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
	})
	require.NoError(t, err)
	require.NotNil(t, h.updatedConfig)
	assert.Equal(t, 21000, h.updatedConfig.Port, "existing proxy port preserved over candidate's")
	assert.Equal(t, 22000, h.updatedConfig.TargetPort, "existing target port preserved over candidate's")
}

// TestApplier_Apply_DegradesLegacyHostIsolation verifies that upgrading a legacy
// config that persisted network isolation together with a host network mode
// degrades (isolation dropped) rather than failing: the upgrade path
// intentionally does not pass WithNetworkIsolationExplicit. See #5775.
func TestApplier_Apply_DegradesLegacyHostIsolation(t *testing.T) {
	t.Parallel()
	h := newApplierHarness(t)

	old := &runner.RunConfig{
		Name:               "wl",
		Image:              "ghcr.io/example/server:1.0.0",
		RegistryServerName: applyServerName,
		Group:              "default",
		Transport:          transporttypes.TransportTypeStreamableHTTP,
		ProxyMode:          transporttypes.ProxyModeStreamableHTTP,
		Port:               21000,
		TargetPort:         22000,
		IsolateNetwork:     true,
		PermissionProfile: &permissions.Profile{
			Network: &permissions.NetworkPermissions{Mode: "host"},
		},
		EnvVars: map[string]string{},
	}
	h.loadConfig = old
	h.provider.EXPECT().
		GetServer(applyServerName).
		Return(&regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}, nil)
	candidate := &regtypes.ImageMetadata{Image: "ghcr.io/example/server:1.2.0"}
	candidate.Transport = string(transporttypes.TransportTypeStreamableHTTP)
	h.resolveImageURL = "ghcr.io/example/server:1.2.0"
	h.resolveMeta = candidate
	h.configMock.EXPECT().GetConfig().Return(&appconfig.Config{}).AnyTimes()
	h.expectUpdate()

	_, err := h.applier.Apply(context.Background(), "wl", ApplyOptions{
		EnvVarValidator: &runner.DetachedEnvVarValidator{},
	})
	require.NoError(t, err, "legacy host+isolation config must degrade, not fail, on upgrade")
	require.NotNil(t, h.updatedConfig)
	assert.False(t, h.updatedConfig.IsolateNetwork,
		"isolation must be dropped when upgrading a host-network workload")
}
