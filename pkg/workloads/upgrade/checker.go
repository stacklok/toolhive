// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// Checker determines whether registry-sourced workloads have an available
// upgrade by comparing their current image and configuration against the
// metadata the injected registry provider reports.
type Checker struct {
	provider registry.Provider
}

// NewChecker creates a Checker backed by the given registry provider.
//
// The provider is the source of truth for candidate image metadata; callers
// typically pass the shared singleton from registry.GetDefaultProvider so the
// provider's response cache is reused across checks. It returns an error if the
// provider is nil.
func NewChecker(provider registry.Provider) (*Checker, error) {
	if provider == nil {
		return nil, fmt.Errorf("registry provider must not be nil")
	}
	return &Checker{provider: provider}, nil
}

// Check evaluates a single workload's RunConfig against the registry and
// returns the upgrade status. It never mutates the supplied config. Per-item
// problems (missing server, unparsable tags, non-image entries) are encoded in
// the returned CheckResult's Status/Reason rather than returned as an error;
// an error is returned only for an invalid call (nil config).
func (c *Checker) Check(_ context.Context, cfg *runner.RunConfig) (*CheckResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("run config must not be nil")
	}

	result := &CheckResult{
		WorkloadName:   cfg.Name,
		RegistryServer: cfg.RegistryServerName,
		CurrentImage:   cfg.Image,
	}

	if cfg.RegistryServerName == "" {
		result.Status = StatusNotRegistrySourced
		return result, nil
	}

	server, err := c.provider.GetServer(cfg.RegistryServerName)
	if err != nil {
		if errors.Is(err, registry.ErrServerNotFound) {
			result.Status = StatusServerNotFound
			return result, nil
		}
		// Keep the detailed provider error out of the result: Reason is
		// serialized into the HTTP response, and for an unreachable or
		// misconfigured registry the raw error can carry internal addressing
		// (e.g. "dial tcp 10.x.x.x:443: ..."). Log it for operators instead.
		slog.Debug("registry lookup failed", "server", cfg.RegistryServerName, "error", err)
		result.Status = StatusUnknown
		result.Reason = "registry lookup failed"
		return result, nil
	}

	imgMeta, ok := server.(*regtypes.ImageMetadata)
	if !ok {
		result.Status = StatusUnknown
		result.Reason = fmt.Sprintf("registry entry %q is not a container image (cannot determine upgrade)", cfg.RegistryServerName)
		return result, nil
	}

	result.CandidateImage = imgMeta.Image

	comparison, reason := compareImageTags(cfg.Image, imgMeta.Image)
	switch comparison {
	case comparisonNewer:
		result.Status = StatusUpgradeAvailable
		result.EnvVarDrift = computeEnvDrift(cfg, imgMeta)
		result.ConfigDrift = computeConfigDrift(cfg, imgMeta)
	case comparisonSameOrOlder:
		result.Status = StatusUpToDate
	case comparisonUndecidable:
		result.Status = StatusUnknown
		result.Reason = reason
	default:
		// Defensive: a future tagComparison value (or an unset zero value) must
		// not fall through to the least-safe StatusUpToDate. Treat anything
		// unexpected as unknown.
		result.Status = StatusUnknown
	}

	return result, nil
}

// CheckAll evaluates a batch of workloads. It never returns an error: each
// workload's outcome (including per-item failures) is encoded in its own
// CheckResult. The returned slice preserves the input order. Nil entries in the
// input are skipped.
func (c *Checker) CheckAll(ctx context.Context, configs []*runner.RunConfig) []*CheckResult {
	results := make([]*CheckResult, 0, len(configs))
	for _, cfg := range configs {
		if cfg == nil {
			continue
		}
		// Check only errors on a nil config, which we already guarded against,
		// so the error here is unreachable; encode defensively rather than drop.
		res, err := c.Check(ctx, cfg)
		if err != nil {
			slog.Debug("upgrade check failed", "workload", cfg.Name, "error", err)
			res = &CheckResult{
				WorkloadName: cfg.Name,
				Status:       StatusUnknown,
				Reason:       "check failed",
			}
		}
		results = append(results, res)
	}
	return results
}

// computeEnvDrift reports the candidate environment variables the workload does
// not currently satisfy. A variable is considered satisfied if it appears as a
// plain env var key in the config, or as the target of one of the config's
// secret parameters. Removed is left unpopulated (best-effort, forward-compat).
//
// It treats the config as read-only. Returns nil when there is no drift.
func computeEnvDrift(cfg *runner.RunConfig, imgMeta *regtypes.ImageMetadata) *EnvVarDrift {
	satisfied := make(map[string]struct{}, len(cfg.EnvVars)+len(cfg.Secrets))
	for k := range cfg.EnvVars {
		satisfied[k] = struct{}{}
	}
	for _, s := range cfg.Secrets {
		parsed, err := secrets.ParseSecretParameter(s)
		if err != nil {
			// Malformed secret parameters can't satisfy a variable; skip them.
			continue
		}
		if parsed.Target != "" {
			satisfied[parsed.Target] = struct{}{}
		}
	}

	var added []EnvVarInfo
	for _, ev := range imgMeta.EnvVars {
		if ev == nil {
			continue
		}
		if _, ok := satisfied[ev.Name]; ok {
			continue
		}
		added = append(added, toEnvVarInfo(ev))
	}

	if len(added) == 0 {
		return nil
	}
	return &EnvVarDrift{Added: added}
}

// computeConfigDrift reports posture differences between the workload's current
// configuration and the candidate registry entry. Each field is nil when that
// aspect did not drift or could not be compared.
//
// The permission profile is compared against imgMeta.Permissions.Name (a
// *permissions.Profile, not a string). Comparison degrades gracefully: when the
// candidate has no profile, or the workload's profile is a custom name/path
// that has no registry analogue, that dimension is not reported as drift unless
// both sides are known and differ. It treats the config as read-only.
func computeConfigDrift(cfg *runner.RunConfig, imgMeta *regtypes.ImageMetadata) *ConfigDrift {
	drift := &ConfigDrift{}

	// Transport: compare the workload's transport string against the registry
	// entry's transport. GetTransport() may return an empty string when the
	// registry entry does not declare one; only report drift when both are set.
	currentTransport := cfg.Transport.String()
	candidateTransport := imgMeta.GetTransport()
	if candidateTransport != "" && currentTransport != "" && currentTransport != candidateTransport {
		drift.Transport = &StringChange{From: currentTransport, To: candidateTransport}
	}

	// Permission profile: compare names. The candidate name is only known when
	// the registry entry carries a profile with a non-empty Name.
	candidateProfile := ""
	if imgMeta.Permissions != nil {
		candidateProfile = imgMeta.Permissions.Name
	}
	currentProfile := cfg.PermissionProfileNameOrPath
	if candidateProfile != "" && currentProfile != "" && currentProfile != candidateProfile {
		drift.PermissionProfile = &StringChange{From: currentProfile, To: candidateProfile}
	}

	if drift.Transport == nil && drift.PermissionProfile == nil {
		return nil
	}
	return drift
}

// toEnvVarInfo converts a registry EnvVar into the drift-report shape, clearing
// the Default value when the variable is a secret to avoid leaking sensitive
// data into reports that may be logged or returned over the API.
func toEnvVarInfo(ev *regtypes.EnvVar) EnvVarInfo {
	info := EnvVarInfo{
		Name:        ev.Name,
		Description: ev.Description,
		Required:    ev.Required,
		Secret:      ev.Secret,
		Default:     ev.Default,
	}
	if info.Secret {
		info.Default = ""
	}
	return info
}
