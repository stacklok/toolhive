// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestAwsStsConverter(t *testing.T) {
	t.Parallel()

	t.Run("StrategyType returns aws_sts", func(t *testing.T) {
		t.Parallel()

		c := &AwsStsConverter{}
		assert.Equal(t, authtypes.StrategyTypeAwsSts, c.StrategyType())
	})

	t.Run("ConvertToStrategy maps all fields", func(t *testing.T) {
		t.Parallel()

		priority := int32(10)
		sessionDuration := int32(1800)
		authConfig := &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeAWSSts,
				AWSSts: &mcpv1beta1.AWSStsConfig{
					Region:          "us-east-1",
					Service:         "execute-api",
					FallbackRoleArn: "arn:aws:iam::123456789012:role/fallback",
					RoleMappings: []mcpv1beta1.RoleMapping{
						{
							Claim:    "admins",
							RoleArn:  "arn:aws:iam::123456789012:role/admin",
							Priority: &priority,
						},
						{
							Matcher: `"devs" in claims["groups"]`,
							RoleArn: "arn:aws:iam::123456789012:role/dev",
						},
					},
					RoleClaim:        "groups",
					SessionDuration:  &sessionDuration,
					SessionNameClaim: "sub",
				},
			},
		}

		c := &AwsStsConverter{}
		strategy, err := c.ConvertToStrategy(authConfig)
		require.NoError(t, err)
		require.NotNil(t, strategy)

		assert.Equal(t, authtypes.StrategyTypeAwsSts, strategy.Type)
		require.NotNil(t, strategy.AwsSts)

		cfg := strategy.AwsSts
		assert.Equal(t, "us-east-1", cfg.Region)
		assert.Equal(t, "execute-api", cfg.Service)
		assert.Equal(t, "arn:aws:iam::123456789012:role/fallback", cfg.FallbackRoleArn)
		assert.Equal(t, "groups", cfg.RoleClaim)
		require.NotNil(t, cfg.SessionDuration)
		assert.Equal(t, int32(1800), *cfg.SessionDuration)
		assert.Equal(t, "sub", cfg.SessionNameClaim)

		require.Len(t, cfg.RoleMappings, 2)
		assert.Equal(t, "admins", cfg.RoleMappings[0].Claim)
		assert.Equal(t, "arn:aws:iam::123456789012:role/admin", cfg.RoleMappings[0].RoleArn)
		require.NotNil(t, cfg.RoleMappings[0].Priority)
		assert.Equal(t, int32(10), *cfg.RoleMappings[0].Priority)
		assert.Equal(t, `"devs" in claims["groups"]`, cfg.RoleMappings[1].Matcher)
		assert.Equal(t, "arn:aws:iam::123456789012:role/dev", cfg.RoleMappings[1].RoleArn)
	})

	t.Run("ConvertToStrategy returns error when AWSSts is nil", func(t *testing.T) {
		t.Parallel()

		authConfig := &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type:   mcpv1beta1.ExternalAuthTypeAWSSts,
				AWSSts: nil,
			},
		}

		c := &AwsStsConverter{}
		strategy, err := c.ConvertToStrategy(authConfig)
		assert.Error(t, err)
		assert.Nil(t, strategy)
		assert.Contains(t, err.Error(), "nil")
	})

	t.Run("ConvertToStrategy copies RoleMappings slice", func(t *testing.T) {
		t.Parallel()

		authConfig := &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeAWSSts,
				AWSSts: &mcpv1beta1.AWSStsConfig{
					Region: "us-west-2",
					RoleMappings: []mcpv1beta1.RoleMapping{
						{Claim: "original", RoleArn: "arn:aws:iam::123456789012:role/original"},
					},
				},
			},
		}

		c := &AwsStsConverter{}
		strategy, err := c.ConvertToStrategy(authConfig)
		require.NoError(t, err)

		// Mutate source slice
		authConfig.Spec.AWSSts.RoleMappings[0].Claim = "mutated"

		// Converted result must be unaffected (independent copy)
		assert.Equal(t, "original", strategy.AwsSts.RoleMappings[0].Claim)
	})

	t.Run("ResolveSecrets is a no-op", func(t *testing.T) {
		t.Parallel()

		scheme := runtime.NewScheme()
		k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		inputStrategy := &authtypes.BackendAuthStrategy{
			Type:   authtypes.StrategyTypeAwsSts,
			AwsSts: &authtypes.AwsStsConfig{Region: "us-east-1"},
		}

		c := &AwsStsConverter{}
		result, err := c.ResolveSecrets(context.Background(), nil, k8sClient, "default", inputStrategy)
		require.NoError(t, err)
		assert.Same(t, inputStrategy, result)
	})
}
