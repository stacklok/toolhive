// Package spectoconfig provides functionality to convert CRD Audit types into audit.Config.
package spectoconfig

import (
	"context"

	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/audit"
)

// ConvertAuditConfig converts the CRD AuditConfig to an audit.Config.
// It may return nil if audit is not enabled.
func ConvertAuditConfig(
	_ context.Context,
	auditConfig *v1alpha1.AuditConfig,
	componentName string,
) *audit.Config {
	if auditConfig == nil || !auditConfig.Enabled {
		return nil
	}

	// When audit is enabled, create a config with defaults and set the component name
	config := audit.DefaultConfig()
	config.Component = componentName

	return config
}
