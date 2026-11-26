// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package externalauthsupport

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/converters"
)

// allAuthTypes mirrors the CRD enum on MCPExternalAuthConfigSpec.Type. If a
// new type is added to the enum without being classified here, the coverage
// assertions below start failing on the registry comparison, which is the
// reminder to think about every consumer.
var allAuthTypes = []mcpv1beta1.ExternalAuthType{
	mcpv1beta1.ExternalAuthTypeTokenExchange,
	mcpv1beta1.ExternalAuthTypeHeaderInjection,
	mcpv1beta1.ExternalAuthTypeBearerToken,
	mcpv1beta1.ExternalAuthTypeUnauthenticated,
	mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
	mcpv1beta1.ExternalAuthTypeAWSSts,
	mcpv1beta1.ExternalAuthTypeUpstreamInject,
	mcpv1beta1.ExternalAuthTypeOBO,
	mcpv1beta1.ExternalAuthTypeXAA,
}

// TestVMCPRowsTrackConverterRegistry pins the property the vMCP-backed rows
// are built on: a type is supported by VirtualMCPServer outgoing auth and
// MCPServerEntry exactly when the converter registry has a converter for it.
// Registering a new converter must extend both rows without any edit here;
// this fails if the derivation wiring is ever replaced with a literal again.
func TestVMCPRowsTrackConverterRegistry(t *testing.T) {
	t.Parallel()

	registered := make(map[mcpv1beta1.ExternalAuthType]struct{})
	for _, authType := range converters.DefaultRegistry().RegisteredTypes() {
		registered[authType] = struct{}{}
	}
	require.NotEmpty(t, registered, "converter registry must have built-in converters")

	for _, authType := range allAuthTypes {
		_, hasConverter := registered[authType]
		assert.Equal(t, hasConverter, Supports(ConsumerVirtualMCPServer, authType),
			"VirtualMCPServer support for %q must equal converter registry membership", authType)
		assert.Equal(t, hasConverter, Supports(ConsumerMCPServerEntry, authType),
			"MCPServerEntry support for %q must equal converter registry membership", authType)
	}
}

// TestDeclaredRowsMatchKnownRuntimeBehavior documents the hand-declared
// MCPServer and MCPRemoteProxy rows. These rows are enforced at the run-config
// dispatch (controllerutil.AddExternalAuthConfigOptions), and the behavioral
// test there drives that dispatch with every type for both consumers — so a
// row edited here without a matching dispatch arm (or vice versa) fails that
// test rather than drifting silently. What this test pins down is the intent:
// which combinations are meant to work.
func TestDeclaredRowsMatchKnownRuntimeBehavior(t *testing.T) {
	t.Parallel()

	assert.True(t, Supports(ConsumerMCPServer, mcpv1beta1.ExternalAuthTypeTokenExchange))
	assert.True(t, Supports(ConsumerMCPServer, mcpv1beta1.ExternalAuthTypeUnauthenticated))
	assert.True(t, Supports(ConsumerMCPServer, mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer))
	assert.True(t, Supports(ConsumerMCPServer, mcpv1beta1.ExternalAuthTypeOBO))
	// bearerToken wires runner.WithRemoteAuth, which only takes effect with a
	// RemoteURL — MCPServer never sets one, so the credential would silently
	// never be injected (#5930).
	assert.False(t, Supports(ConsumerMCPServer, mcpv1beta1.ExternalAuthTypeBearerToken))
	// awsSts fails the empty-RemoteURL check at runtime rather than no-opping;
	// rejecting it at reconcile time gives the user the failure where they can
	// see it.
	assert.False(t, Supports(ConsumerMCPServer, mcpv1beta1.ExternalAuthTypeAWSSts))

	// MCPRemoteProxy is MCPServer plus the remote-only types: it always has a
	// RemoteURL, so bearerToken and awsSts genuinely work there.
	assert.True(t, Supports(ConsumerMCPRemoteProxy, mcpv1beta1.ExternalAuthTypeBearerToken))
	assert.True(t, Supports(ConsumerMCPRemoteProxy, mcpv1beta1.ExternalAuthTypeAWSSts))
}

// TestBackendsAcceptTypesAppliedByVirtualMCPServer pins the types a backend
// carries for a VirtualMCPServer. Discovered mode reads the reference from the
// backend resource, so a backend that rejected one of these types would never
// become ready and the VirtualMCPServer in front of it would lose the backend.
func TestBackendsAcceptTypesAppliedByVirtualMCPServer(t *testing.T) {
	t.Parallel()

	appliedByVMCP := []mcpv1beta1.ExternalAuthType{
		mcpv1beta1.ExternalAuthTypeHeaderInjection,
		mcpv1beta1.ExternalAuthTypeUpstreamInject,
		mcpv1beta1.ExternalAuthTypeXAA,
	}
	for _, consumer := range []Consumer{ConsumerMCPServer, ConsumerMCPRemoteProxy} {
		for _, authType := range appliedByVMCP {
			assert.True(t, Supports(consumer, authType),
				"%s must accept %q so a VirtualMCPServer can discover it", consumer, authType)
			assert.True(t, AppliedByVirtualMCPServer(consumer, authType))
			assert.True(t, Supports(ConsumerVirtualMCPServer, authType),
				"%q is only worth accepting on a backend while VirtualMCPServer can apply it", authType)
		}
		// A type the proxy runner applies itself is not one a VirtualMCPServer
		// applies on its behalf.
		assert.False(t, AppliedByVirtualMCPServer(consumer, mcpv1beta1.ExternalAuthTypeTokenExchange))
	}

	// The vMCP-backed consumers apply their types themselves.
	assert.False(t, AppliedByVirtualMCPServer(ConsumerVirtualMCPServer, mcpv1beta1.ExternalAuthTypeHeaderInjection))
	assert.False(t, AppliedByVirtualMCPServer(ConsumerMCPServerEntry, mcpv1beta1.ExternalAuthTypeHeaderInjection))
}

func TestSupportsUnknownConsumer(t *testing.T) {
	t.Parallel()

	assert.False(t, Supports(Consumer("SomethingElse"), mcpv1beta1.ExternalAuthTypeTokenExchange),
		"an unknown consumer must never report support")
}

func TestValidate(t *testing.T) {
	t.Parallel()

	require.NoError(t, Validate(ConsumerMCPRemoteProxy, mcpv1beta1.ExternalAuthTypeBearerToken))

	err := Validate(ConsumerMCPServer, mcpv1beta1.ExternalAuthTypeBearerToken)
	require.Error(t, err)

	var unsupported *UnsupportedTypeError
	require.True(t, errors.As(err, &unsupported), "Validate must return a typed UnsupportedTypeError")
	assert.Equal(t, ConsumerMCPServer, unsupported.Consumer)
	assert.Equal(t, mcpv1beta1.ExternalAuthTypeBearerToken, unsupported.AuthType)
	assert.Contains(t, err.Error(), "bearerToken")
	assert.Contains(t, err.Error(), "MCPServer")
}
