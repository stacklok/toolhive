// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"log/slog"
	"os"
	"sync"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// EnvEnableUntrustedMode is the environment variable gating the untrusted
// single-tenant mode (one backend pod per (user, session, untrusted
// MCPServer), plus its bump CA, egress-lockdown NetworkPolicy, and sidecar
// data plane). The cost of that tenancy model is too high to be the default,
// so the mode is opt-in: unset or any value other than "true"/"1" means OFF.
// The operator helm chart surfaces it as operator.features.untrustedMode and
// the operator forwards its own value to the vMCP Deployments it creates, so
// one chart value controls both processes.
const EnvEnableUntrustedMode = "TOOLHIVE_ENABLE_UNTRUSTED_MODE"

// MetadataKeyUntrustedOffWarned guards the one-per-process WARN emitted when a
// backend asks to run untrusted while the mode is disabled (the untrusted
// metadata stamp is suppressed and the workload runs trusted). Discovery runs
// per backend; logging per stamp would flood, and the suppressed condition is
// already surfaced per-CR by the operator (UntrustedMode condition + event).
var metadataKeyUntrustedOffWarned sync.Once

// ModeEnabled reports whether untrusted mode is enabled for this process.
// Read per call (never cached): tests toggle the env var, and the variable is
// set at process start and never mutated in production, so the syscall is
// free and staleness is impossible.
func ModeEnabled() bool {
	v := os.Getenv(EnvEnableUntrustedMode)
	return v == "true" || v == "1"
}

// MarkBackend stamps the untrusted identity metadata on a discovered vMCP
// backend when — and only when — the MCPServer opts in (spec.untrusted) AND
// the mode is enabled for this process. When the mode is disabled the stamp
// is suppressed: the resolver never provisions a per-session pod for the
// backend and it is served through the trusted shared StatefulSet. The stamp
// is centralised here because the metadata keys are consumed across package
// boundaries (cli gate, session manager, resolver) and must not be set
// anywhere else.
func MarkBackend(mcpServer *mcpv1beta1.MCPServer, metadata map[string]string) {
	if !mcpServer.Spec.Untrusted {
		return
	}
	if !ModeEnabled() {
		metadataKeyUntrustedOffWarned.Do(func() {
			slog.Warn("spec.untrusted=true but untrusted mode is disabled; "+
				"the workload is served through the trusted multi-tenant path (no per-session pods, no egress broker)",
				"env_var", EnvEnableUntrustedMode, "mcpserver", mcpServer.Name)
		})
		return
	}
	metadata[MetadataKeyUntrusted] = "true"
	metadata[MetadataKeyMCPServerUID] = string(mcpServer.UID)
}
