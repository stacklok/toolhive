// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// OBO validation messages. They name neither a Secret nor a value, so they are
// safe to surface verbatim in a status condition.
const (
	msgOBOConfigRequired   = "obo: configuration is required for the obo external auth type"
	msgOBOClientIDRequired = "obo: clientId is required"
	//nolint:gosec // G101: validation message, not a credential.
	msgOBOClientSecretRef  = "obo: clientSecretRef must set both name and key"
	msgOBOCacheSkew        = "obo: cacheSkew must not be negative"
	msgOBOCacheSkewTooBig  = "obo: cacheSkew is too large"
	msgOBOAudienceOrScopes = "obo: at least one of audience or scopes is required"

	// MsgOBORequiresIncomingAuth is the condition message for an obo external
	// auth config referenced by an MCPServer or MCPRemoteProxy that has no
	// incoming OIDC authentication.
	MsgOBORequiresIncomingAuth = "obo external auth requires incoming authentication: " +
		"set spec.oidcConfigRef so the proxy has an authenticated user token to exchange"
)

// OBOHandler bundles the three operator-time OBO dispatch points.
//
// Deprecated: OBO is built in. OBOHandler and RegisterOBOHandler are kept only
// so out-of-tree code that still registers a handler keeps compiling; they will
// be removed once out-of-tree users are gone, see stacklok/toolhive#7200. A
// registered handler applies to MCPServer and MCPRemoteProxy only. Its
// SecretEnvVars is deliberately not used for VirtualMCPServer, which always
// mounts each OBO client secret under the built-in per-config name
// (OBOVMCPSecretEnvVars) so that two OBO backends in one vMCP pod never
// collide on a single env var name.
type OBOHandler struct {
	// Validate verifies an obo-typed MCPExternalAuthConfig is well-formed.
	Validate func(*mcpv1beta1.MCPExternalAuthConfig) error

	// ApplyRunConfig appends the OBO runner options for a consuming MCPServer
	// or MCPRemoteProxy.
	ApplyRunConfig func(
		ctx context.Context, c client.Client, namespace string,
		cfg *mcpv1beta1.MCPExternalAuthConfig,
		opts *[]runner.RunConfigBuilderOption,
	) error

	// SecretEnvVars returns the pod env vars the OBO proxy middleware needs.
	SecretEnvVars func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error)
}

// oboMu guards reads and writes of oboHandler.
var oboMu sync.RWMutex

// oboHandler holds the OBO dispatch functions: the built-in implementation
// unless out-of-tree code replaced it through the deprecated RegisterOBOHandler.
var oboHandler = OBOHandler{
	Validate:       validateOBO,
	ApplyRunConfig: applyOBORunConfig,
	SecretEnvVars:  oboProxySecretEnvVars,
}

// RegisterOBOHandler replaces the built-in OBO handler. Last-write-wins.
// Panics if any of the three function fields is nil.
//
// Deprecated: OBO is built in; it will be removed once out-of-tree users are
// gone, see stacklok/toolhive#7200. As documented on OBOHandler, the
// registered SecretEnvVars does not apply to VirtualMCPServer.
func RegisterOBOHandler(h OBOHandler) {
	switch {
	case h.Validate == nil:
		panic("controllerutil.RegisterOBOHandler: Validate is nil")
	case h.ApplyRunConfig == nil:
		panic("controllerutil.RegisterOBOHandler: ApplyRunConfig is nil")
	case h.SecretEnvVars == nil:
		panic("controllerutil.RegisterOBOHandler: SecretEnvVars is nil")
	}
	oboMu.Lock()
	defer oboMu.Unlock()
	oboHandler = h
}

// currentOBOHandler returns a snapshot of the handler under the read lock.
func currentOBOHandler() OBOHandler {
	oboMu.RLock()
	defer oboMu.RUnlock()
	return oboHandler
}

// OBOValidate validates an obo-typed MCPExternalAuthConfig. Every failure the
// user can fix by editing the spec is an *obo.ValidationError, whose message is
// safe to surface in a status condition.
func OBOValidate(cfg *mcpv1beta1.MCPExternalAuthConfig) error {
	return currentOBOHandler().Validate(cfg)
}

// OBOSecretEnvVars returns the env var that mounts the OBO client secret into
// a proxyrunner pod (MCPServer or MCPRemoteProxy) under obo.ClientSecretEnvVar.
// The vMCP pod uses per-config names instead; see GenerateUniqueOBOSecretEnvVarName.
func OBOSecretEnvVars(cfg *mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
	return currentOBOHandler().SecretEnvVars(cfg)
}

// OBOApplyRunConfig appends the OBO middleware config for a consuming MCPServer
// or MCPRemoteProxy to opts. A malformed config is an *obo.ValidationError; a
// referenced Secret that is not available yet is a plain (transient) error.
func OBOApplyRunConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	cfg *mcpv1beta1.MCPExternalAuthConfig,
	opts *[]runner.RunConfigBuilderOption,
) error {
	return currentOBOHandler().ApplyRunConfig(ctx, c, namespace, cfg, opts)
}

// ValidateOBOIncomingAuth rejects an obo-typed MCPExternalAuthConfig referenced
// by an MCPServer or MCPRemoteProxy that has no incoming OIDC authentication.
// OBO exchanges the authenticated user's token, so without incoming
// authentication every request would fail with 401 at runtime. Returns nil for
// every other external auth type.
func ValidateOBOIncomingAuth(cfg *mcpv1beta1.MCPExternalAuthConfig, hasIncomingOIDC bool) error {
	if cfg == nil || cfg.Spec.Type != mcpv1beta1.ExternalAuthTypeOBO || hasIncomingOIDC {
		return nil
	}
	return &obo.ValidationError{Message: MsgOBORequiresIncomingAuth}
}

// AddOBOSecretEnvVars resolves ref to an MCPExternalAuthConfig and, if it is
// the obo type, returns the proxyrunner pod env var carrying its client secret.
// For every other type it returns no env vars: the other types' secret env vars
// are produced by their own per-type helpers (GenerateTokenExchangeEnvVars,
// GenerateBearerTokenEnvVar), using consumer-specific names that must stay
// byte-identical. Do not add the other types here.
//
// MCPServer and MCPRemoteProxy both route through this helper, so the
// deployment builder and the drift check compute the same env.
func AddOBOSecretEnvVars(
	ctx context.Context,
	c client.Client,
	namespace string,
	ref *mcpv1beta1.ExternalAuthConfigRef,
) ([]corev1.EnvVar, error) {
	if ref == nil {
		return nil, nil
	}

	externalAuthConfig, err := GetExternalAuthConfigByName(ctx, c, namespace, ref.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	if externalAuthConfig.Spec.Type != mcpv1beta1.ExternalAuthTypeOBO {
		return nil, nil
	}
	return OBOSecretEnvVars(externalAuthConfig)
}

// OBOVMCPSecretEnvVars returns the env var that mounts an OBO client secret
// into the vMCP pod. The name is unique per MCPExternalAuthConfig
// (GenerateUniqueOBOSecretEnvVarName) so several OBO backends in one vMCP never
// collide. It never dispatches through a handler registered with the deprecated
// RegisterOBOHandler: a handler's SecretEnvVars returns a single fixed name.
func OBOVMCPSecretEnvVars(cfg *mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
	if oboSpec(cfg) == nil {
		return nil, &obo.ValidationError{Message: msgOBOConfigRequired}
	}
	if err := validateOBOSecretRef(cfg.Spec.OBO.ClientSecretRef); err != nil {
		return nil, err
	}
	return []corev1.EnvVar{oboSecretEnvVar(GenerateUniqueOBOSecretEnvVarName(cfg.Name), cfg.Spec.OBO.ClientSecretRef)}, nil
}

// validateOBO is the built-in OBOHandler.Validate.
func validateOBO(cfg *mcpv1beta1.MCPExternalAuthConfig) error {
	_, err := buildOBOMiddlewareParameters(oboSpec(cfg))
	return err
}

// applyOBORunConfig is the built-in OBOHandler.ApplyRunConfig. It builds the
// obo.MiddlewareParameters contract, verifies the referenced client Secret is
// present, and appends the "obo"-typed MiddlewareConfig through the runner's
// AdditionalMiddlewareConfigs seam. It does not mutate opts on error.
func applyOBORunConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	cfg *mcpv1beta1.MCPExternalAuthConfig,
	opts *[]runner.RunConfigBuilderOption,
) error {
	params, err := buildOBOMiddlewareParameters(oboSpec(cfg))
	if err != nil {
		return err
	}
	if err := verifyOBOClientSecret(ctx, c, namespace, cfg.Spec.OBO.ClientSecretRef); err != nil {
		return err
	}

	mwConfig, err := types.NewMiddlewareConfig(obo.MiddlewareType, params)
	if err != nil {
		return fmt.Errorf("obo: build middleware config: %w", err)
	}
	*opts = append(*opts, runner.WithAdditionalMiddlewareConfigs(mwConfig))
	return nil
}

// oboProxySecretEnvVars is the built-in OBOHandler.SecretEnvVars. The env var
// name matches the contract's clientSecretEnvVar; the secret value never
// enters the RunConfig ConfigMap.
func oboProxySecretEnvVars(cfg *mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
	if oboSpec(cfg) == nil {
		return nil, &obo.ValidationError{Message: msgOBOConfigRequired}
	}
	if err := validateOBOSecretRef(cfg.Spec.OBO.ClientSecretRef); err != nil {
		return nil, err
	}
	return []corev1.EnvVar{oboSecretEnvVar(obo.ClientSecretEnvVar, cfg.Spec.OBO.ClientSecretRef)}, nil
}

// buildOBOMiddlewareParameters maps an OBOConfig onto obo.MiddlewareParameters
// and validates what the CRD schema cannot express: a non-blank clientId,
// a clientSecretRef with a non-blank name and key, a cacheSkew between zero and
// math.MaxInt32 seconds, a usable tenant and authority, and at least one
// non-blank audience or scope. Every rejection is an *obo.ValidationError. The
// cacheSkew bounds match the vMCP OBO converter (cacheSkewToSeconds), so a
// config the vMCP would reject is InvalidConfig at reconcile time.
func buildOBOMiddlewareParameters(oboCfg *mcpv1beta1.OBOConfig) (*obo.MiddlewareParameters, error) {
	if oboCfg == nil {
		return nil, &obo.ValidationError{Message: msgOBOConfigRequired}
	}
	if strings.TrimSpace(oboCfg.ClientID) == "" {
		return nil, &obo.ValidationError{Message: msgOBOClientIDRequired}
	}
	if err := validateOBOSecretRef(oboCfg.ClientSecretRef); err != nil {
		return nil, err
	}
	if oboCfg.CacheSkew != nil {
		if oboCfg.CacheSkew.Duration < 0 {
			return nil, &obo.ValidationError{Message: msgOBOCacheSkew}
		}
		if int64(oboCfg.CacheSkew.Duration/time.Second) > math.MaxInt32 {
			return nil, &obo.ValidationError{Message: msgOBOCacheSkewTooBig}
		}
	}

	// entra.TokenURL is the single source of truth for the tenant/authority to
	// token endpoint mapping; its messages carry no secret, so surface them.
	tokenURL, err := entra.TokenURL(oboCfg.TenantID, oboCfg.Authority)
	if err != nil {
		return nil, &obo.ValidationError{Message: err.Error()}
	}

	params := &obo.MiddlewareParameters{
		TokenURL:                 tokenURL,
		ClientID:                 oboCfg.ClientID,
		Audience:                 oboCfg.Audience,
		Scopes:                   oboCfg.Scopes,
		ClientSecretEnvVar:       obo.ClientSecretEnvVar,
		SubjectTokenProviderName: oboCfg.SubjectTokenProviderName,
	}
	// A sub-second skew truncates to 0, which the runtime treats as "use the
	// cache default". The vMCP converter truncates the same way.
	if oboCfg.CacheSkew != nil {
		params.CacheSkewSeconds = int(oboCfg.CacheSkew.Seconds())
	}

	if params.ExchangeTarget() == "" {
		return nil, &obo.ValidationError{Message: msgOBOAudienceOrScopes}
	}
	return params, nil
}

// validateOBOSecretRef rejects an absent or incomplete clientSecretRef. OBO is
// a confidential-client (shared-secret) flow, so both the Secret name and key
// are required.
func validateOBOSecretRef(ref *mcpv1beta1.SecretKeyRef) error {
	if ref == nil || strings.TrimSpace(ref.Name) == "" || strings.TrimSpace(ref.Key) == "" {
		return &obo.ValidationError{Message: msgOBOClientSecretRef}
	}
	return nil
}

// verifyOBOClientSecret confirms the referenced Secret exists and carries the
// key. A missing Secret or key is transient (the user may still create it), so
// it returns a plain error the reconciler requeues, not an *obo.ValidationError.
func verifyOBOClientSecret(ctx context.Context, c client.Client, namespace string, ref *mcpv1beta1.SecretKeyRef) error {
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, &secret); err != nil {
		return fmt.Errorf("obo: referenced client secret is not available: %w", err)
	}
	if _, ok := secret.Data[ref.Key]; !ok {
		return fmt.Errorf("obo: referenced client secret is missing the configured key")
	}
	return nil
}

// oboSpec returns cfg's OBO spec, or nil when cfg itself is nil.
func oboSpec(cfg *mcpv1beta1.MCPExternalAuthConfig) *mcpv1beta1.OBOConfig {
	if cfg == nil {
		return nil
	}
	return cfg.Spec.OBO
}

// oboSecretEnvVar builds an env var named name that sources its value from ref.
func oboSecretEnvVar(name string, ref *mcpv1beta1.SecretKeyRef) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
				Key:                  ref.Key,
			},
		},
	}
}
