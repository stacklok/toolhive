// Package runconfig provides functions to build RunConfigBuilder options for audit configuration.
// Given the size of this file, it's probably better suited to merge with another. This can be
// done when the runconfig has been fully moved into this package.
package runconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
)

// TestAddAuditConfigOptions tests the addition of audit configuration options to the RunConfigBuilder
func TestAddAuditConfigOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mcpServer *mcpv1alpha1.MCPServer
		expected  func(t *testing.T, config *runner.RunConfig)
	}{
		{
			name: "with disabled audit configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "audit-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					ProxyPort: 8080,
					Audit: &mcpv1alpha1.AuditConfig{
						Enabled: true,
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "audit-server", config.Name)

				// Verify telemetry config is set
				assert.NotNil(t, config.AuditConfig)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			options := []runner.RunConfigBuilderOption{
				runner.WithName(tt.mcpServer.Name),
				runner.WithImage(tt.mcpServer.Spec.Image),
			}
			AddAuditConfigOptions(&options, tt.mcpServer.Spec.Audit)

			rc, err := runner.NewOperatorRunConfigBuilder(context.Background(), nil, nil, nil, options...)
			assert.NoError(t, err)

			tt.expected(t, rc)
		})
	}
}
