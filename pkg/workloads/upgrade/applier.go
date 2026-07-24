// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// resolveFunc resolves an MCP server definition from the registry and verifies
// its image provenance. It mirrors retriever.ResolveMCPServer so the real
// implementation can be swapped for a stub in tests.
type resolveFunc func(
	ctx context.Context,
	serverOrImage string,
	rawCACertPath string,
	verificationType string,
	groupName string,
	runtimeOverride *templates.RuntimeConfig,
) (string, regtypes.ServerMetadata, error)

// enforcePullFunc runs the policy gate and performs a verified image pull. It
// mirrors retriever.EnforcePolicyAndPullImage so the real implementation can be
// swapped for a stub in tests.
type enforcePullFunc func(
	ctx context.Context,
	runConfig *runner.RunConfig,
	serverMetadata regtypes.ServerMetadata,
	imageURL string,
	puller retriever.ImagePuller,
	pullTimeout time.Duration,
	locallyBuilt bool,
) error

// loadStateFunc loads a workload's persisted RunConfig. It mirrors
// runner.LoadState so the real implementation can be swapped in tests.
type loadStateFunc func(ctx context.Context, name string) (*runner.RunConfig, error)

// ErrApplyAfterDestroy marks an Apply failure that occurred AFTER the
// destructive recreate began (the manager has already started to stop/delete
// the workload). Unlike preparation failures — which leave the running workload
// untouched — a failure tagged with this sentinel means the workload may be
// stopped, deleted, or only partially recreated: its state is uncertain and the
// failure is NOT safely retryable against an intact workload. Callers use
// errors.Is(err, ErrApplyAfterDestroy) to distinguish this 5xx-class condition
// from the 4xx-class "candidate could not be prepared" failures.
var ErrApplyAfterDestroy = errors.New("upgrade failed after workload recreate began; workload state is uncertain")

// Applier is the canonical, security-critical path for applying an available
// registry-sourced upgrade to a running workload. It re-resolves and verifies
// the candidate image, pulls it, and only then asks the workload manager to
// recreate the workload with the new image.
//
// Applier is the single place where an upgrade is materialized; both the CLI
// (Phase D2) and the API (Phase D3) delegate here so that the verify-then-pull
// ordering and TOCTOU guard live in exactly one place.
type Applier struct {
	manager   workloads.Manager
	checker   *Checker
	appConfig config.Provider

	// resolveFn, enforcePullFn, and loadStateFn wrap the corresponding
	// package-level functions in pkg/runner and pkg/runner/retriever. They are
	// always populated in production (set in NewApplier) and exist purely so the
	// package-level functions can be substituted in unit tests. They are NOT
	// optional hooks: NewApplier never leaves them nil.
	resolveFn     resolveFunc
	enforcePullFn enforcePullFunc
	loadStateFn   loadStateFunc
}

// NewApplier creates an Applier.
//
// All dependencies are required and validated; the constructor fails loudly
// rather than producing an Applier that would panic or silently no-op at apply
// time. The appConfig provider supplies the registry source URLs recorded on
// the upgraded workload's config.
func NewApplier(manager workloads.Manager, checker *Checker, appConfig config.Provider) (*Applier, error) {
	if manager == nil {
		return nil, fmt.Errorf("workload manager must not be nil")
	}
	if checker == nil {
		return nil, fmt.Errorf("checker must not be nil")
	}
	if appConfig == nil {
		return nil, fmt.Errorf("config provider must not be nil")
	}
	return &Applier{
		manager:       manager,
		checker:       checker,
		appConfig:     appConfig,
		resolveFn:     retriever.ResolveMCPServer,
		enforcePullFn: retriever.EnforcePolicyAndPullImage,
		loadStateFn:   runner.LoadState,
	}, nil
}

// Apply upgrades the named workload to the candidate image the registry
// currently reports for the workload's registry server.
//
// Sequence (see RFC THV-0068 design §4; step numbers match the inline comments):
//  1. Load the workload's current RunConfig (treated as read-only).
//  2. Re-run the upgrade check against the registry. If the workload is not in
//     StatusUpgradeAvailable, the current CheckResult is returned unchanged as a
//     no-op (NOT an error) so the caller can decide how to message it. The check
//     is ALWAYS re-derived here; a CheckResult computed earlier by the caller is
//     never trusted, closing the time-of-check/time-of-use window.
//  3. Re-resolve and VERIFY the candidate image from the registry by server
//     name (passing the name, not the image ref, is what triggers provenance
//     verification inside ResolveMCPServer).
//  4. Reject non-image (remote) registry entries: there is nothing to pull and
//     recreate, so refuse rather than risk destroying the workload.
//  5. Build a merged RunConfig that preserves the workload's user configuration
//     while bumping the image to the candidate and re-validating env vars
//     against the candidate's fresh metadata.
//  6. Run the policy gate and perform a verified pull of the candidate image.
//  7. Only after all of the above succeed, ask the manager to recreate the
//     workload with the new config.
//
// No rollback: steps 3-6 all happen BEFORE the destructive recreate in step 7,
// so any failure while preparing the candidate (resolution, verification,
// policy, pull) leaves the running workload completely untouched. Once step 7
// begins, the manager deletes and recreates the workload; there is no automatic
// revert to the previous image or configuration if recreation fails. Recovery
// is a forward operation (re-running the previous configuration explicitly).
//
// Runtime boundary: the "verified pull before destruction" guarantee is precise
// only for local container runtimes. On Kubernetes, EnforcePolicyAndPullImage
// runs the verification and policy gate but DELEGATES the byte-level image pull
// to the kubelet, which happens AFTER the workload is recreated. In all cases,
// verification and the policy gate always precede destruction; only the local
// runtime additionally guarantees the bytes are present before destruction.
// (Local runtimes are the scope of this phase; the boundary is documented for
// completeness.)
//
// On success the *CheckResult that drove the upgrade is returned (including any
// detected drift) so the caller can report what changed.
func (a *Applier) Apply(ctx context.Context, name string, opts ApplyOptions) (*CheckResult, error) {
	// 1. Load current state. Treat as strictly read-only: never mutate old or
	// any of its maps/slices.
	old, err := a.loadStateFn(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to load state for workload %q: %w", name, err)
	}

	// 2. Re-check against the registry. Never trust a CheckResult supplied by the
	// caller; always re-derive here (TOCTOU guard).
	res, err := a.checker.Check(ctx, old)
	if err != nil {
		return nil, fmt.Errorf("failed to check workload %q for upgrade: %w", name, err)
	}
	if res.Status != StatusUpgradeAvailable {
		// No-op: nothing to apply. Return the result, not an error, so the caller
		// decides how to message "already up to date" / "not upgradable".
		return res, nil
	}

	// 3. Resolve + verify the candidate image from the registry. Passing the
	// registry SERVER NAME (not the image ref) is what triggers provenance
	// verification inside ResolveMCPServer. A failure here leaves the running
	// workload untouched.
	imageURL, serverMeta, err := a.resolveFn(
		ctx,
		old.RegistryServerName,
		opts.CACertPath,
		opts.defaultVerifySetting(),
		"", // registry-based group lookups are not supported
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve candidate image for workload %q: %w", name, err)
	}

	// 4. Only container-image servers can be upgraded in place. A remote
	// (non-image) entry has nothing to pull/recreate; refuse rather than risk
	// destroying the workload.
	imgMeta, ok := serverMeta.(*regtypes.ImageMetadata)
	if !ok || imgMeta == nil {
		return nil, fmt.Errorf(
			"registry server %q is not a container image; cannot upgrade workload %q in place",
			old.RegistryServerName, name,
		)
	}

	// 5. Build the merged config off copies of the caller's/old config's data.
	newConfig, err := a.buildUpgradedConfig(ctx, old, imgMeta, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to build upgraded config for workload %q: %w", name, err)
	}

	// 6. Policy gate + verified pull BEFORE any destruction. EnforcePolicyAndPullImage
	// runs the policy gate AND the pull, so we deliberately do NOT also call
	// EagerCheckCreateServer. This is an intentional deviation from design v2 §4
	// step 5 (which listed a separate EagerCheckCreateServer call): running both
	// would double-gate the same config against the same policy. A failure here
	// still leaves the running workload untouched.
	if err := a.enforcePullFn(ctx, newConfig, serverMeta, imageURL, retriever.PullMCPServerImage, 0, false); err != nil {
		return nil, fmt.Errorf("failed to prepare candidate image for workload %q: %w", name, err)
	}

	// 7. Destructive apply. The manager stops, deletes, and recreates the
	// workload with the new config. We sever request-scoped cancellation with
	// context.WithoutCancel so an upgrade in progress is not aborted mid-recreate
	// by a client disconnect or handler deadline, while still carrying the OTel
	// trace span and request-scoped values (which context.Background would drop).
	//
	// Errors from here on are tagged with ErrApplyAfterDestroy: the workload may
	// already be stopped/deleted, so the caller must treat its state as uncertain
	// rather than assuming the running workload was left untouched.
	completion, err := a.manager.UpdateWorkload(context.WithoutCancel(ctx), name, newConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to update workload %q: %w", name, errors.Join(err, ErrApplyAfterDestroy))
	}

	// Block until the stop/delete/save/start cycle finishes. UpdateWorkload runs
	// it asynchronously and returns a CompletionFunc; both callers are
	// synchronous (the CLI prints success and the API responds 200 only once the
	// upgrade has actually taken effect), and the new RunConfig — including the
	// upgraded image — is only persisted once this completes. Returning before it
	// finishes would surface a stale, pre-upgrade workload to the caller.
	if completion != nil {
		if err := completion(); err != nil {
			return nil, fmt.Errorf("failed to complete upgrade for workload %q: %w", name, errors.Join(err, ErrApplyAfterDestroy))
		}
	}

	return res, nil
}

// buildUpgradedConfig constructs the RunConfig for the upgraded workload.
//
// An upgrade PRESERVES the entire user configuration and changes ONLY:
//   - the image (bumped to the candidate),
//   - the environment variables and secrets (old merged with the caller's
//     ApplyOptions),
//   - the registry source URLs (re-resolved from the current app config).
//
// Everything else is byte-identical to the workload's current config: a
// round-trip upgrade of a fully-configured workload must not silently drop
// fields such as OIDC/authz/audit/telemetry config, tool filters, middleware,
// transport/posture, etc. Dropping a field like ToolsFilter (which would
// broaden the exposed tool surface) or the authz/audit config is a security
// regression, not mere behavior drift.
//
// Posture fields (transport, permission profile, network isolation) are
// PRESERVED from the current config and merely SURFACED as drift by the checker
// (preserve-and-warn); they are NOT converged to the candidate's posture here.
//
// The builder is still given the candidate imageMetadata so the env-var
// validator runs against the FRESH metadata: newly-required env vars on the
// candidate are detected/handled (the env-var drift goal of RFC THV-0068).
//
// It never mutates old or any of its maps/slices: caller-visible data is cloned
// before use, and fields copied post-build are either value types or pointers
// that the upgraded config and old config may safely share read-only (neither
// the manager nor the proxy mutates them in place).
func (a *Applier) buildUpgradedConfig(
	ctx context.Context,
	old *runner.RunConfig,
	imgMeta *regtypes.ImageMetadata,
	opts ApplyOptions,
) (*runner.RunConfig, error) {
	// Copy-before-mutate: never touch the caller's/old config's underlying data.
	mergedEnv := maps.Clone(old.EnvVars)
	if mergedEnv == nil {
		mergedEnv = make(map[string]string, len(opts.EnvVars))
	}
	maps.Copy(mergedEnv, opts.EnvVars)

	mergedSecrets := mergeSecrets(old.Secrets, opts.Secrets)

	cfg := a.appConfig.GetConfig()
	regAPIURL, regURL := runner.ResolveRegistrySourceURLs(imgMeta, cfg)

	options := []runner.RunConfigBuilderOption{
		// WithImage performs the version bump: NewRunConfigBuilder does NOT derive
		// RunConfig.Image from the imageMetadata argument, so it must be set
		// explicitly to the candidate image (mirrors workload_service.go).
		runner.WithImage(imgMeta.Image),
		runner.WithName(old.Name),
		runner.WithGroup(old.Group),
		// Transport/ports/posture are preserved from old (preserve-and-warn). The
		// builder still resolves/normalizes them (and sets transport env vars)
		// against the same values old already had.
		runner.WithTransportAndPorts(old.Transport.String(), old.Port, old.TargetPort),
		runner.WithExistingPort(old.Port),
		runner.WithHost(old.Host),
		runner.WithVolumes(slices.Clone(old.Volumes)),
		runner.WithSecrets(mergedSecrets),
		// Intentionally NOT paired with WithNetworkIsolationExplicit: a legacy
		// config with isolation + a host/none network mode should degrade-and-warn
		// on upgrade, not hard-fail. See #5775.
		runner.WithNetworkIsolation(old.IsolateNetwork),
		runner.WithAllowDockerGateway(old.AllowDockerGateway),
		runner.WithTrustProxyHeaders(old.TrustProxyHeaders),
		runner.WithStrictProtocolValidation(old.StrictProtocolValidation),
		runner.WithProxyMode(old.ProxyMode),
		runner.WithCmdArgs(slices.Clone(old.CmdArgs)),
		runner.WithStateless(old.Stateless),
		runner.WithEndpointPrefix(old.EndpointPrefix),
		runner.WithRegistrySourceURLs(regAPIURL, regURL),
		runner.WithRegistryServerName(old.RegistryServerName),
	}

	// Permission profile: if the workload pinned a named/path profile, preserve
	// that name/path (do NOT re-pin to the resolved object or to the candidate's
	// profile) so it stays consistent with what the checker reports as drift
	// (the checker compares PermissionProfileNameOrPath). Otherwise preserve the
	// resolved profile object.
	if old.PermissionProfileNameOrPath != "" {
		options = append(options, runner.WithPermissionProfileNameOrPath(old.PermissionProfileNameOrPath))
	} else if old.PermissionProfile != nil {
		// Clone before handing it over: processVolumeMounts appends to Read/Write,
		// which would otherwise mutate old's profile through the shared pointer and
		// break this builder's "never mutates old" contract. The clone is shallow
		// except for Read/Write: the builder only ever appends to those two slices
		// here. The shared Network pointer is safe today because the builder mutates
		// it solely via WithNetworkMode, which buildUpgradedConfig never sets; if a
		// future change adds WithNetworkMode (or the builder starts touching Network
		// unconditionally), Network must be cloned here too.
		cloned := *old.PermissionProfile
		cloned.Read = slices.Clone(old.PermissionProfile.Read)
		cloned.Write = slices.Clone(old.PermissionProfile.Write)
		options = append(options, runner.WithPermissionProfile(&cloned))
	} else {
		options = append(options, runner.WithPermissionProfile(old.PermissionProfile))
	}

	newConfig, err := runner.NewRunConfigBuilder(ctx, imgMeta, mergedEnv, opts.EnvVarValidator, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to build run config: %w", err)
	}

	// Preserve the remaining user-owned fields that have no clean builder option
	// (or whose option carries side effects we don't want). Without this, any
	// such field would be silently dropped on upgrade because UpdateWorkload
	// persists newConfig verbatim.
	preserveUserConfigFields(newConfig, old)

	return newConfig, nil
}

// preserveUserConfigFields copies user-owned RunConfig fields from old onto the
// freshly-built upgraded config. It covers every persisted, user-configurable
// field that the builder-option list above does NOT already set, so a
// round-trip upgrade changes only the image, env/secrets, and registry URLs.
//
// SchemaVersion, Image, Name, Group, Transport, Host, Port, TargetPort,
// Volumes, Secrets, EnvVars, ProxyMode, CmdArgs, Stateless, EndpointPrefix,
// network/posture flags, permission profile, container name/labels (standard
// labels), and registry fields are intentionally NOT copied here: they are
// produced by the builder above (with image/env/secrets/registry URLs being the
// deliberate deltas).
//
// It does not mutate old. Pointer and slice/map fields are shared read-only;
// neither the workload manager nor the proxy mutates these structures in place.
func preserveUserConfigFields(dst, old *runner.RunConfig) {
	// Middleware-driving / security-relevant config.
	dst.OIDCConfig = old.OIDCConfig
	dst.AuthzConfig = old.AuthzConfig
	dst.AuthzConfigPath = old.AuthzConfigPath
	dst.AuditConfig = old.AuditConfig
	dst.AuditConfigPath = old.AuditConfigPath
	dst.TelemetryConfig = old.TelemetryConfig
	dst.TokenExchangeConfig = old.TokenExchangeConfig
	dst.AWSStsConfig = old.AWSStsConfig
	dst.UpstreamSwapConfig = old.UpstreamSwapConfig
	dst.EmbeddedAuthServerConfig = old.EmbeddedAuthServerConfig

	// Tool exposure. Dropping these would broaden the exposed tool surface.
	// ToolsFilter is replaced wholesale by the manager/proxy (never appended to),
	// so sharing the slice is safe; ToolsOverride is cloned so the contract holds
	// strictly even if a future caller mutates the map in place.
	dst.ToolsFilter = old.ToolsFilter
	dst.ToolsOverride = maps.Clone(old.ToolsOverride)

	// The pre-built middleware chain. It is derived from the fields above; carry
	// it over verbatim so the upgraded workload keeps the exact same chain rather
	// than silently losing all middleware (the builder only rebuilds the chain
	// via WithMiddlewareFromFlags, which we do not call).
	dst.MiddlewareConfigs = old.MiddlewareConfigs
	dst.ValidatingWebhooks = old.ValidatingWebhooks
	dst.MutatingWebhooks = old.MutatingWebhooks

	// Rate limiting.
	dst.RateLimitConfig = old.RateLimitConfig
	dst.RateLimitNamespace = old.RateLimitNamespace

	// Remote server config (preserved for completeness; image upgrades are local,
	// but a workload may carry these and they must not be dropped).
	dst.RemoteURL = old.RemoteURL
	dst.RemoteAuthConfig = old.RemoteAuthConfig
	dst.HeaderForward = old.HeaderForward

	// TargetHost and Publish: the builder never sets TargetHost (WithTargetHost
	// re-derives it from RemoteURL, which is not what we want), and Publish is
	// not passed as a builder option, so both must be copied directly from old.
	dst.TargetHost = old.TargetHost
	dst.Publish = old.Publish

	// Proxy session / runtime / scheduling knobs.
	dst.SessionTTL = old.SessionTTL
	dst.RuntimeConfig = old.RuntimeConfig
	dst.IgnoreConfig = old.IgnoreConfig
	dst.K8sPodTemplatePatch = old.K8sPodTemplatePatch
	dst.ScalingConfig = old.ScalingConfig
	dst.MCPServerGeneration = old.MCPServerGeneration

	// Debug flag and any user-set container labels not produced by the builder's
	// standard-label step.
	dst.Debug = old.Debug
	for k, v := range old.ContainerLabels {
		if _, ok := dst.ContainerLabels[k]; !ok {
			dst.ContainerLabels[k] = v
		}
	}

	// Deprecated/no-longer-used fields, preserved for byte-level fidelity.
	dst.EnvFileDir = old.EnvFileDir
	dst.ThvCABundle = old.ThvCABundle
	dst.JWKSAuthTokenFile = old.JWKSAuthTokenFile
}

// mergeSecrets returns a new slice containing the old secret parameters plus any
// of the additional parameters whose target is not already covered. It never
// mutates the input slices. Parameters that fail to parse are preserved as-is
// from old and skipped from additions (a malformed addition can't be deduped
// safely and is dropped rather than silently shadowing a valid existing one).
func mergeSecrets(oldSecrets, additional []string) []string {
	merged := slices.Clone(oldSecrets)

	existingTargets := make(map[string]struct{}, len(oldSecrets))
	for _, s := range oldSecrets {
		parsed, err := secrets.ParseSecretParameter(s)
		if err != nil {
			continue
		}
		if parsed.Target != "" {
			existingTargets[parsed.Target] = struct{}{}
		}
	}

	for _, s := range additional {
		parsed, err := secrets.ParseSecretParameter(s)
		if err != nil {
			// A malformed addition can't be deduped; drop it rather than risk
			// shadowing an existing valid target. This is explicit user input, so
			// warn (logging only the parameter string, never any secret value).
			slog.Warn("dropping malformed secret parameter from upgrade", "parameter", s)
			continue
		}
		if parsed.Target != "" {
			if _, ok := existingTargets[parsed.Target]; ok {
				continue
			}
			existingTargets[parsed.Target] = struct{}{}
		}
		merged = append(merged, s)
	}

	return merged
}
