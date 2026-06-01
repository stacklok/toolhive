// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
)

// UpgradeStatus is the outcome of an upgrade check for a single workload.
//
//revive:disable-next-line:exported // "UpgradeStatus" reads better than "Status" at call sites.
type UpgradeStatus string

const (
	// StatusUpToDate indicates the workload is already running the candidate
	// image reported by the registry (or a newer one).
	StatusUpToDate UpgradeStatus = "up-to-date"

	// StatusUpgradeAvailable indicates the registry reports a newer image than
	// the one the workload is currently running.
	StatusUpgradeAvailable UpgradeStatus = "upgrade-available"

	// StatusNotRegistrySourced indicates the workload was not created from a
	// registry entry (no RegistryServerName), so no upgrade can be determined.
	StatusNotRegistrySourced UpgradeStatus = "not-registry-sourced"

	// StatusServerNotFound indicates the workload references a registry server
	// name that no longer exists in the configured registry.
	StatusServerNotFound UpgradeStatus = "server-not-found"

	// StatusUnknown indicates the upgrade status could not be determined, e.g.
	// the registry lookup failed, the entry is a remote (non-image) server, or
	// the image tags are not comparable. The Reason field explains why.
	StatusUnknown UpgradeStatus = "unknown"
)

// CheckResult is the outcome of checking a single workload for an available
// upgrade. EnvVarDrift and ConfigDrift are only populated when an upgrade is
// available and the relevant drift was detected.
type CheckResult struct {
	// WorkloadName is the name of the workload that was checked.
	WorkloadName string `json:"workload_name"`

	// RegistryServer is the registry entry name the workload was sourced from.
	// Empty when the workload is not registry-sourced.
	RegistryServer string `json:"registry_server,omitempty"`

	// Status is the upgrade status for the workload.
	Status UpgradeStatus `json:"status"`

	// CurrentImage is the image reference the workload is currently running.
	CurrentImage string `json:"current_image,omitempty"`

	// CandidateImage is the image reference the registry currently reports.
	CandidateImage string `json:"candidate_image,omitempty"`

	// Reason provides additional context, primarily for StatusUnknown.
	Reason string `json:"reason,omitempty"`

	// EnvVarDrift describes environment variables the candidate registry entry
	// declares that differ from the workload's current configuration.
	EnvVarDrift *EnvVarDrift `json:"env_var_drift,omitempty"`

	// ConfigDrift describes posture differences (transport, network isolation,
	// permission profile) between the workload and the candidate registry entry.
	ConfigDrift *ConfigDrift `json:"config_drift,omitempty"`
}

// EnvVarDrift describes how the candidate registry entry's declared
// environment variables differ from those the workload currently satisfies.
type EnvVarDrift struct {
	// Added lists environment variables the candidate declares that the
	// workload does not currently supply (via plain env vars or secrets).
	Added []EnvVarInfo `json:"added,omitempty"`

	// Removed lists environment variables the workload supplies that the
	// candidate no longer declares. Populated on a best-effort basis; may be
	// empty even when removals exist (forward-compatible field).
	Removed []EnvVarInfo `json:"removed,omitempty"`
}

// EnvVarInfo is a registry-declared environment variable surfaced in drift.
type EnvVarInfo struct {
	// Name is the environment variable name.
	Name string `json:"name"`

	// Description is the human-readable purpose of the variable.
	Description string `json:"description,omitempty"`

	// Required indicates whether the candidate marks the variable as required.
	Required bool `json:"required"`

	// Secret indicates whether the variable holds sensitive data.
	Secret bool `json:"secret,omitempty"`

	// Default is the candidate's default value. It is cleared (left empty)
	// whenever Secret is true: a secret env var's default could carry sensitive
	// data, and surfacing it in a drift report (which may be logged or returned
	// over the API) would leak it. Non-secret defaults are safe to display.
	Default string `json:"default,omitempty"`
}

// ConfigDrift describes posture differences between a workload's current
// configuration and the candidate registry entry. A nil field means that
// aspect did not drift (or could not be compared).
type ConfigDrift struct {
	// Transport is set when the candidate's transport differs from the
	// workload's current transport.
	Transport *StringChange `json:"transport,omitempty"`

	// NetworkIsolation is set when the candidate's network isolation posture
	// differs from the workload's current setting.
	NetworkIsolation *BoolChange `json:"network_isolation,omitempty"`

	// PermissionProfile is set when the candidate's permission profile differs
	// from the workload's current profile.
	PermissionProfile *StringChange `json:"permission_profile,omitempty"`
}

// StringChange records a string-valued configuration change from the
// workload's current value to the candidate registry value.
type StringChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// BoolChange records a boolean-valued configuration change from the workload's
// current value to the candidate registry value.
type BoolChange struct {
	From bool `json:"from"`
	To   bool `json:"to"`
}

// ApplyOptions controls how an upgrade is applied to a workload.
//
// It is defined here for use by the Applier (Phase D). No apply logic is
// implemented in this phase.
type ApplyOptions struct {
	// EnvVars are additional or overriding environment variables to merge into
	// the upgraded workload's configuration.
	EnvVars map[string]string

	// Secrets are additional secret parameters (`<name>,target=<env>`) to merge
	// into the upgraded workload's configuration.
	Secrets []string

	// EnvVarValidator validates that required environment variables and secrets
	// are supplied for the candidate registry entry.
	EnvVarValidator runner.EnvVarValidator

	// VerifySetting controls image signature verification. Empty defaults to
	// retriever.VerifyImageWarn.
	VerifySetting string

	// CACertPath is an optional path to a CA certificate bundle used when
	// resolving the candidate image from a registry over TLS.
	CACertPath string
}

// defaultVerifySetting returns the configured verification setting, falling
// back to retriever.VerifyImageWarn when unset.
func (o ApplyOptions) defaultVerifySetting() string {
	if o.VerifySetting == "" {
		return retriever.VerifyImageWarn
	}
	return o.VerifySetting
}
