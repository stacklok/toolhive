// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
	"github.com/stacklok/toolhive/pkg/auth/obo/exchange"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// OBOConverter converts an MCPExternalAuthConfig of type "obo" into the vMCP
// BackendAuthStrategy consumed by strategies.OBOStrategy.
type OBOConverter struct{}

// StrategyType returns the vMCP strategy identifier for OBO.
func (*OBOConverter) StrategyType() string { return authtypes.StrategyTypeOBO }

// ConvertToStrategy translates an obo-typed MCPExternalAuthConfig into a vMCP
// BackendAuthStrategy. It maps the CRD's tenantId+authority into the fully-formed
// Entra token endpoint (via entra.TokenURL — the single source of truth shared
// with the operator OBO validation) and copies clientId, audience, scopes,
// subjectTokenProviderName, and the cache skew through to authtypes.OBOConfig.
//
// It fails on a malformed config: a missing clientId or clientSecretRef (the
// shared-secret flow requires both), neither audience nor scopes (no
// exchange target), a missing/invalid tenant or authority, or a negative cache
// skew — mirroring the operator's reconcile-time OBO validation for those
// fields so the two surfaces accept the same configs. SubjectTokenProviderName's
// format is enforced by the CRD schema at admission, so it is passed through
// here unvalidated rather than re-checked.
//
// Neither the client secret nor ClientSecretEnv is set here. In inline mode the
// operator sets ClientSecretEnv to the per-config name it mounts the secret
// under (controllerutil.GenerateUniqueOBOSecretEnvVarName), so several OBO backends in
// one vMCP pod never share an env var. In discovered mode ResolveSecrets sets
// the resolved ClientSecret instead.
func (*OBOConverter) ConvertToStrategy(
	externalAuth *mcpv1beta1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	cfg := externalAuth.Spec.OBO
	if cfg == nil {
		return nil, fmt.Errorf("vMCP OBO converter: %s", msgOBONil)
	}

	// Check the simple required credentials first, so a config missing a client
	// credential reports that rather than a downstream tenant-format error.
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("vMCP OBO converter: %s", msgClientID)
	}
	// OBO is a confidential-client shared-secret flow: clientSecretRef must be
	// present with a non-empty name and key. Mirrors the operator OBO validation's
	// validateSecretRef depth so the two surfaces reject the same refs (a
	// {name:"", key:""} ref passes the CRD's presence-only Required markers).
	if ref := cfg.ClientSecretRef; ref == nil ||
		strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Key) == "" {
		return nil, fmt.Errorf("vMCP OBO converter: %s", msgClientSecretRef)
	}

	// Reuse the single audience/scopes collapse implementation to check that a
	// target exists, rather than re-inspecting the fields inline — so the
	// converter's notion of "has a target" is exactly what the runtime strategy
	// will send to Entra.
	if exchange.Target(cfg.Audience, cfg.Scopes) == "" {
		return nil, fmt.Errorf("vMCP OBO converter: %s", msgAudienceOrScopes)
	}

	tokenURL, err := entra.TokenURL(cfg.TenantID, cfg.Authority)
	if err != nil {
		return nil, fmt.Errorf("vMCP OBO converter: %w", err)
	}

	cacheSkewSeconds, err := cacheSkewToSeconds(cfg.CacheSkew)
	if err != nil {
		return nil, fmt.Errorf("vMCP OBO converter: %w", err)
	}

	return &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeOBO,
		OBO: &authtypes.OBOConfig{
			TokenURL:                 tokenURL,
			ClientID:                 cfg.ClientID,
			Audience:                 cfg.Audience,
			Scopes:                   cfg.Scopes,
			SubjectTokenProviderName: cfg.SubjectTokenProviderName,
			CacheSkewSeconds:         cacheSkewSeconds,
		},
	}, nil
}

// ResolveSecrets fetches the client secret referenced by the OBO config's
// clientSecretRef from Kubernetes and sets it on the strategy (discovered-auth
// mode, where secrets cannot be mounted as pod env vars because the vMCP pod does
// not know about backend auth configs at creation time). It clears ClientSecretEnv
// so the resolved value is the single secret source, and returns the strategy
// unchanged when no secret reference is configured.
//
// The secret value is never logged and never written to the strategy's
// non-secret fields, so it does not leak into logs or CR status; error messages
// name the secret and key, never the value.
func (*OBOConverter) ResolveSecrets(
	ctx context.Context,
	externalAuth *mcpv1beta1.MCPExternalAuthConfig,
	k8sClient client.Client,
	namespace string,
	strategy *authtypes.BackendAuthStrategy,
) (*authtypes.BackendAuthStrategy, error) {
	if strategy == nil || strategy.OBO == nil {
		return nil, fmt.Errorf("vMCP OBO converter: %s", msgStrategyNil)
	}

	cfg := externalAuth.Spec.OBO
	if cfg == nil {
		return nil, fmt.Errorf("vMCP OBO converter: %s", msgOBONil)
	}

	// Nothing to resolve without a secret reference. ConvertToStrategy requires
	// clientSecretRef for the v1 flow, so this guards the standalone-call path.
	if cfg.ClientSecretRef == nil {
		return strategy, nil
	}

	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{Name: cfg.ClientSecretRef.Name, Namespace: namespace}
	if err := k8sClient.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("vMCP OBO converter: get secret %s/%s: %w",
			namespace, cfg.ClientSecretRef.Name, err)
	}

	value, ok := secret.Data[cfg.ClientSecretRef.Key]
	if !ok {
		return nil, fmt.Errorf("vMCP OBO converter: secret %s/%s has no key %s",
			namespace, cfg.ClientSecretRef.Name, cfg.ClientSecretRef.Key)
	}

	strategy.OBO.ClientSecretEnv = ""
	strategy.OBO.ClientSecret = string(value)
	return strategy, nil
}

// cacheSkewToSeconds converts the CRD's metav1.Duration cache skew into the
// vMCP contract's integer-seconds pointer. A nil skew leaves the cache default
// (returns nil); a negative skew is rejected, matching the operator OBO validation and
// avoiding the cache constructor's negative-skew panic at request time.
//
// The duration is truncated toward zero to whole seconds — identical to the
// operator's BuildOBOMiddlewareParameters (int(d.Seconds())) — so the same
// CRD value yields the same seconds on both surfaces. A sub-second skew therefore
// becomes 0, which the runtime treats as "use the cache default"; this matches
// the operator/runtime path exactly. The result is bounded: a duration whose
// whole-seconds value would overflow int32 is rejected loudly rather than
// wrapping to a wrong (or negative) value in the contract.
func cacheSkewToSeconds(d *metav1.Duration) (*int32, error) {
	if d == nil {
		return nil, nil
	}
	if d.Duration < 0 {
		return nil, fmt.Errorf("%s", msgCacheSkewNegative)
	}
	seconds := int64(d.Duration / time.Second) // truncates toward zero
	if seconds > math.MaxInt32 {
		return nil, fmt.Errorf("%s", msgCacheSkewTooLarge)
	}
	v := int32(seconds) //nolint:gosec // G115: bounded to [0, MaxInt32] by the negative and MaxInt32 checks above
	return &v, nil
}

// Validation messages, spelled once so the converter and its tests share the
// exact literals and the linter does not flag repeated string constants.
const (
	msgOBONil            = "obo configuration is nil"
	msgStrategyNil       = "obo strategy is nil"
	msgClientID          = "clientId is required"
	msgClientSecretRef   = "clientSecretRef must set a name and key"
	msgAudienceOrScopes  = "one of audience or scopes is required"
	msgCacheSkewNegative = "cacheSkew must not be negative"
	msgCacheSkewTooLarge = "cacheSkew is too large"
)
