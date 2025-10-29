// Package runconfig provides functions to build RunConfigBuilder options for audit configuration.
// Given the size of this file, it's probably better suited to merge with another. This can be
// done when the runconfig has been fully moved into this package.
package runconfig

import (
	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
)

// AddAuditConfigOptions adds audit configuration options to the builder options
func AddAuditConfigOptions(
	options *[]runner.RunConfigBuilderOption,
	auditConfig *mcpv1alpha1.AuditConfig,
) {
	if auditConfig == nil {
		return
	}

	// Add audit config to options with default config (no custom config path for now)
	*options = append(*options, runner.WithAuditEnabled(auditConfig.Enabled, ""))
}
