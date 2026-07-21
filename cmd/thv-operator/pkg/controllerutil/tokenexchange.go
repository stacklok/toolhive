// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/pkg/auth/awssts"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/oauthproto/tokenexchange"
	"github.com/stacklok/toolhive/pkg/runner"
)

// OBOHandler bundles the three operator-time dispatch points for OBO-typed
// MCPExternalAuthConfig resources. An out-of-tree build replaces the default
// instance (which returns obo.ErrEnterpriseRequired from every method) by
// calling RegisterOBOHandler once during init().
//
// # Error contract
//
// Every method below must return one of three error categories so callers can
// triage failures consistently:
//
//   - errors.Is(err, obo.ErrEnterpriseRequired) — the build is not licensed
//     to run OBO. Callers treat this as permanent until an out-of-tree handler
//     is registered.
//   - errors.As(err, &*obo.ValidationError) — the user-supplied spec is
//     malformed (missing field, schema violation, invalid URL). Callers treat
//     this as permanent until the user edits the spec. The ValidationError's
//     Message field is written verbatim into the surfaced condition, so
//     handler authors must ensure it is safe to expose (no Secret names, no
//     internal addressing, no credential fragments).
//   - anything else — treated as a transient failure (Secret not yet
//     available, JWKS unreachable, webhook 5xx). Callers requeue with backoff
//     rather than locking the resource into a permanent state.
//
// Returning a non-ValidationError for what is genuinely a user-fix condition
// causes the reconciler to spin on backoff. Returning a ValidationError for
// what is genuinely transient locks the resource into InvalidConfig until the
// user edits the spec. Handler authors are responsible for placing each
// failure in the right bucket.
type OBOHandler struct {
	// Validate is called from MCPExternalAuthConfig validation to verify the
	// resource's obo-typed config is well-formed. See the type-level "Error
	// contract" doc for the three-bucket triage callers apply to its return.
	Validate func(*mcpv1beta1.MCPExternalAuthConfig) error

	// ApplyRunConfig is called from AddExternalAuthConfigOptions to apply
	// OBO-specific runner configuration options for consuming MCPServer/
	// MCPRemoteProxy resources. See the type-level "Error contract" doc.
	ApplyRunConfig func(
		ctx context.Context, c client.Client, namespace string,
		cfg *mcpv1beta1.MCPExternalAuthConfig,
		opts *[]runner.RunConfigBuilderOption,
	) error

	// SecretEnvVars is called when computing the consuming resource's pod
	// environment, to inject any secrets the OBO flow needs at runtime. See
	// the type-level "Error contract" doc.
	SecretEnvVars func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error)
}

// oboMu guards reads and writes of oboHandler. Reads dispatched through the
// exported OBOValidate / OBOSecretEnvVars / OBOApplyRunConfig wrappers take an
// RLock; RegisterOBOHandler takes the write lock. Production reads today all
// happen on a single reconcile-loop goroutine, but the lock is here so that
// out-of-tree builds adding a hot-reload feature or admission-webhook
// re-registration do not introduce a latent data race.
var oboMu sync.RWMutex

// oboHandler holds the package-level OBO handler. The default implementation
// returns obo.ErrEnterpriseRequired from each method; an out-of-tree build
// replaces it via RegisterOBOHandler. Access only through currentOBOHandler
// (read) or RegisterOBOHandler (write) so the mutex contract is preserved.
var oboHandler = OBOHandler{
	Validate: func(*mcpv1beta1.MCPExternalAuthConfig) error { return obo.ErrEnterpriseRequired },
	ApplyRunConfig: func(context.Context, client.Client, string,
		*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption) error {
		return obo.ErrEnterpriseRequired
	},
	SecretEnvVars: func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
		return nil, obo.ErrEnterpriseRequired
	},
}

// currentOBOHandler returns a snapshot of the registered handler under the
// read lock so callers can dispatch through it without holding any lock for
// the duration of the call.
func currentOBOHandler() OBOHandler {
	oboMu.RLock()
	defer oboMu.RUnlock()
	return oboHandler
}

// RegisterOBOHandler replaces the package-level OBO handler. It is intended
// to be called exactly once during init() in an out-of-tree package that
// blank-imports controllerutil. Calling it more than once is allowed and
// last-write-wins; no panic on double-register, matching the existing
// pkg/config.RegisterProviderFactory precedent.
//
// Panics if any of the three function fields is nil — a partial registration
// would nil-deref deep inside dispatch, far from the call site. Surfacing the
// problem at process start (init() time) is far easier to diagnose than at
// reconcile time.
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

// OBOValidate runs the registered OBO handler's Validate function on the
// supplied MCPExternalAuthConfig. With the default handler it returns
// obo.ErrEnterpriseRequired.
func OBOValidate(cfg *mcpv1beta1.MCPExternalAuthConfig) error {
	return currentOBOHandler().Validate(cfg)
}

// OBOSecretEnvVars runs the registered OBO handler's SecretEnvVars function.
// With the default handler it returns (nil, obo.ErrEnterpriseRequired).
func OBOSecretEnvVars(cfg *mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
	return currentOBOHandler().SecretEnvVars(cfg)
}

// OBOApplyRunConfig runs the registered OBO handler's ApplyRunConfig function.
// With the default handler it returns obo.ErrEnterpriseRequired without
// mutating the supplied options slice. Exported so that the three-method
// surface stays symmetric with OBOValidate / OBOSecretEnvVars; routes through
// the package-level OBO handler registered via RegisterOBOHandler.
func OBOApplyRunConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	cfg *mcpv1beta1.MCPExternalAuthConfig,
	opts *[]runner.RunConfigBuilderOption,
) error {
	return currentOBOHandler().ApplyRunConfig(ctx, c, namespace, cfg, opts)
}

// GenerateTokenExchangeEnvVars generates environment variables for token exchange
func GenerateTokenExchangeEnvVars(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfigRef *mcpv1beta1.ExternalAuthConfigRef,
	getExternalAuthConfig func(context.Context, client.Client, string, string) (*mcpv1beta1.MCPExternalAuthConfig, error),
) ([]corev1.EnvVar, error) {
	var envVars []corev1.EnvVar

	if externalAuthConfigRef == nil {
		return envVars, nil
	}

	externalAuthConfig, err := getExternalAuthConfig(ctx, c, namespace, externalAuthConfigRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	if externalAuthConfig == nil {
		return nil, fmt.Errorf("MCPExternalAuthConfig %s not found", externalAuthConfigRef.Name)
	}

	if externalAuthConfig.Spec.Type != mcpv1beta1.ExternalAuthTypeTokenExchange {
		return envVars, nil
	}

	tokenExchangeSpec := externalAuthConfig.Spec.TokenExchange
	if tokenExchangeSpec == nil {
		return envVars, nil
	}

	// Only add client secret env var if ClientSecretRef is provided
	if tokenExchangeSpec.ClientSecretRef != nil {
		envVars = append(envVars, corev1.EnvVar{
			Name: "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: tokenExchangeSpec.ClientSecretRef.Name,
					},
					Key: tokenExchangeSpec.ClientSecretRef.Key,
				},
			},
		})
	}

	return envVars, nil
}

// AddExternalAuthConfigOptions adds external authentication configuration options to builder options
// This creates token exchange configuration which will be automatically converted to middleware by
// PopulateMiddlewareConfigs() when the runner starts. This ensures correct middleware ordering.
//
// The oidcConfig parameter is used for embedded auth server configuration to populate:
//   - AllowedAudiences from oidcConfig.ResourceURL
//   - ScopesSupported from oidcConfig.Scopes
//
// For embedded auth server type, oidcConfig is REQUIRED and must have ResourceURL set.
func AddExternalAuthConfigOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	mcpServerName string,
	externalAuthConfigRef *mcpv1beta1.ExternalAuthConfigRef,
	oidcConfig *oidc.OIDCConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	if externalAuthConfigRef == nil {
		return nil
	}

	// Fetch the MCPExternalAuthConfig
	externalAuthConfig, err := GetExternalAuthConfigByName(ctx, c, namespace, externalAuthConfigRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	// Handle different auth types
	switch externalAuthConfig.Spec.Type {
	case mcpv1beta1.ExternalAuthTypeTokenExchange:
		return addTokenExchangeConfig(ctx, c, namespace, externalAuthConfig, options)
	case mcpv1beta1.ExternalAuthTypeHeaderInjection:
		return addHeaderInjectionConfig(ctx, c, namespace, externalAuthConfig, options)
	case mcpv1beta1.ExternalAuthTypeBearerToken:
		return addBearerTokenConfig(ctx, c, namespace, externalAuthConfig, options)
	case mcpv1beta1.ExternalAuthTypeUnauthenticated:
		// No config to add for unauthenticated
		return nil
	case mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer:
		return AddEmbeddedAuthServerConfigOptions(ctx, c, namespace, mcpServerName, externalAuthConfigRef, oidcConfig, options)
	case mcpv1beta1.ExternalAuthTypeAWSSts:
		return addAWSStsConfig(externalAuthConfig, options)
	case mcpv1beta1.ExternalAuthTypeUpstreamInject:
		// Upstream inject is handled by the vMCP converter at runtime
		return nil
	case mcpv1beta1.ExternalAuthTypeOBO:
		// Dispatch through the registered handler. In upstream-only builds the
		// default handler returns obo.ErrEnterpriseRequired; an out-of-tree
		// build registers a real handler via RegisterOBOHandler. Bypass the
		// default's "unsupported external auth type" path so callers can
		// distinguish via errors.Is(err, obo.ErrEnterpriseRequired).
		return OBOApplyRunConfig(ctx, c, namespace, externalAuthConfig, options)
	case mcpv1beta1.ExternalAuthTypeXAA:
		// XAA is handled by the vMCP converter at runtime
		return nil
	default:
		return fmt.Errorf("unsupported external auth type: %s", externalAuthConfig.Spec.Type)
	}
}

// AddOBOSecretEnvVars resolves ref to an MCPExternalAuthConfig and, if it is the
// obo type, returns the pod environment variables the registered OBOHandler asks
// for at runtime. For every other type it returns no env vars: this is the
// OBO-only secret-env dispatcher, deliberately narrower than its sibling
// AddExternalAuthConfigOptions (which switches on every ExternalAuthType). The
// name says "OBO" precisely so it is not mistaken for full type coverage —
// MCPServer / MCPRemoteProxy / VirtualMCPServer each produce the non-obo types'
// secret env vars through their own per-type helpers (GenerateTokenExchangeEnvVars,
// GenerateBearerTokenEnvVar, VirtualMCPServer's getExternalAuthConfigSecretEnvVars
// switch), using consumer-specific env var names that must stay byte-identical.
// Do NOT add the other types here, and do NOT drop the existing per-type calls
// assuming this helper covers them.
//
// obo is the one type a single dispatcher can own, because its env var name is
// defined by the handler and is therefore identical across consumers; the others
// are not. Routing the obo path through here is what keeps consumers from
// drifting — the failure mode #5537 fixed.
//
// A build without a registered OBO handler yields obo.ErrEnterpriseRequired from
// the dispatch; that is treated as "no env vars" (obo is inert) rather than an
// error, so the deployment builder and drift-check paths compute identical env
// and the Deployment does not enter a reconcile hot-loop. The
// MCPExternalAuthConfig reconciler is what surfaces the enterprise-required state
// to the user. (VirtualMCPServer intentionally does NOT route through this
// wrapper: its dispatch must propagate ErrEnterpriseRequired per #5328, so it
// calls OBOSecretEnvVars directly — but it likewise forwards every env var.)
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

	envVars, err := OBOSecretEnvVars(externalAuthConfig)
	if errors.Is(err, obo.ErrEnterpriseRequired) {
		// No OBO handler registered (upstream-only build): obo is inert. Return
		// no env vars so the builder and drift paths agree; the
		// MCPExternalAuthConfig reconciler surfaces EnterpriseRequired.
		return nil, nil
	}
	// Forward every env var the handler returns.
	return envVars, err
}

func addTokenExchangeConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	tokenExchangeSpec := externalAuthConfig.Spec.TokenExchange
	if tokenExchangeSpec == nil {
		return fmt.Errorf("token exchange configuration is nil for type tokenExchange")
	}

	// Validate that the referenced Kubernetes secret exists (if ClientSecretRef is provided)
	if tokenExchangeSpec.ClientSecretRef != nil {
		var secret corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      tokenExchangeSpec.ClientSecretRef.Name,
		}, &secret); err != nil {
			return fmt.Errorf("failed to get client secret %s/%s: %w",
				namespace, tokenExchangeSpec.ClientSecretRef.Name, err)
		}

		if _, ok := secret.Data[tokenExchangeSpec.ClientSecretRef.Key]; !ok {
			return fmt.Errorf("client secret %s/%s is missing key %q",
				namespace, tokenExchangeSpec.ClientSecretRef.Name, tokenExchangeSpec.ClientSecretRef.Key)
		}
	}

	// Determine header strategy based on ExternalTokenHeaderName
	headerStrategy := "replace" // Default strategy
	if tokenExchangeSpec.ExternalTokenHeaderName != "" {
		headerStrategy = "custom"
	}

	// Normalize SubjectTokenType to full URN (accepts both short forms and full URNs)
	normalizedTokenType, err := tokenexchange.NormalizeTokenType(tokenExchangeSpec.SubjectTokenType)
	if err != nil {
		return fmt.Errorf("invalid subject token type: %w", err)
	}

	// Build token exchange configuration
	// Client secret is provided via TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET environment variable
	// to avoid embedding plaintext secrets in the ConfigMap
	tokenExchangeConfig := &tokenexchange.Config{
		TokenURL:                tokenExchangeSpec.TokenURL,
		ClientID:                tokenExchangeSpec.ClientID,
		Audience:                tokenExchangeSpec.Audience,
		Scopes:                  tokenExchangeSpec.Scopes,
		SubjectTokenType:        normalizedTokenType,
		HeaderStrategy:          headerStrategy,
		ExternalTokenHeaderName: tokenExchangeSpec.ExternalTokenHeaderName,
	}

	// Use WithTokenExchangeConfig to add configuration
	// The middleware will be automatically created by PopulateMiddlewareConfigs() in the correct order
	*options = append(*options, runner.WithTokenExchangeConfig(tokenExchangeConfig))

	return nil
}

// addHeaderInjectionConfig adds header injection configuration to runner options
// For now, this is a no-op as header injection for MCPServer is not implemented
// Header injection is primarily used for vMCP outgoing auth, not for MCPServer incoming auth
func addHeaderInjectionConfig(
	_ context.Context,
	_ client.Client,
	_ string,
	_ *mcpv1beta1.MCPExternalAuthConfig,
	_ *[]runner.RunConfigBuilderOption,
) error {
	// Header injection for MCPServer is not yet implemented
	// This is a placeholder to avoid the "unsupported auth type" error
	// MCPServer's ExternalAuthConfigRef is meant for incoming auth configuration
	// but header injection doesn't make sense in that context
	return nil
}

// addBearerTokenConfig adds bearer token configuration to runner options
func addBearerTokenConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	bearerTokenSpec := externalAuthConfig.Spec.BearerToken
	if bearerTokenSpec == nil {
		return fmt.Errorf("bearer token configuration is nil for type bearerToken")
	}

	if bearerTokenSpec.TokenSecretRef == nil {
		return fmt.Errorf("bearer token configuration is missing TokenSecretRef")
	}

	// Validate secret exists
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      bearerTokenSpec.TokenSecretRef.Name,
	}, &secret); err != nil {
		return fmt.Errorf("failed to get bearer token secret %s/%s: %w",
			namespace, bearerTokenSpec.TokenSecretRef.Name, err)
	}

	// Validate key exists
	if _, ok := secret.Data[bearerTokenSpec.TokenSecretRef.Key]; !ok {
		return fmt.Errorf("bearer token secret %s/%s is missing key %q",
			namespace, bearerTokenSpec.TokenSecretRef.Name, bearerTokenSpec.TokenSecretRef.Key)
	}

	// Convert to CLI format: "secret-name,target=bearer_token"
	// Note: The secret name in CLI format must match the Kubernetes Secret name
	// This will be resolved by EnvironmentProvider looking for TOOLHIVE_SECRET_{secret-name}
	cliFormat := fmt.Sprintf("%s,target=bearer_token", bearerTokenSpec.TokenSecretRef.Name)

	// Create remote auth config
	remoteConfig := &remote.Config{
		BearerToken: cliFormat,
	}

	*options = append(*options, runner.WithRemoteAuth(remoteConfig))
	return nil
}

// GenerateBearerTokenEnvVar generates environment variables for bearer token authentication
func GenerateBearerTokenEnvVar(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfigRef *mcpv1beta1.ExternalAuthConfigRef,
	getExternalAuthConfig func(context.Context, client.Client, string, string) (*mcpv1beta1.MCPExternalAuthConfig, error),
) ([]corev1.EnvVar, error) {
	var envVars []corev1.EnvVar

	if externalAuthConfigRef == nil {
		return envVars, nil
	}

	externalAuthConfig, err := getExternalAuthConfig(ctx, c, namespace, externalAuthConfigRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	if externalAuthConfig == nil {
		return nil, fmt.Errorf("MCPExternalAuthConfig %s not found", externalAuthConfigRef.Name)
	}

	if externalAuthConfig.Spec.Type != mcpv1beta1.ExternalAuthTypeBearerToken {
		return envVars, nil
	}

	bearerTokenSpec := externalAuthConfig.Spec.BearerToken
	if bearerTokenSpec == nil || bearerTokenSpec.TokenSecretRef == nil {
		return envVars, nil
	}

	// Environment variable name: TOOLHIVE_SECRET_{secret-name}
	envVarName := fmt.Sprintf("TOOLHIVE_SECRET_%s", bearerTokenSpec.TokenSecretRef.Name)

	envVars = append(envVars, corev1.EnvVar{
		Name: envVarName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: bearerTokenSpec.TokenSecretRef.Name,
				},
				Key: bearerTokenSpec.TokenSecretRef.Key,
			},
		},
	})

	return envVars, nil
}

// addAWSStsConfig adds AWS STS configuration to runner options
// This enables OIDC token exchange for AWS credentials using AssumeRoleWithWebIdentity
func addAWSStsConfig(
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	awsStsSpec := externalAuthConfig.Spec.AWSSts
	if awsStsSpec == nil {
		return fmt.Errorf("awsSts configuration is nil for type awsSts")
	}

	// Convert role mappings from CRD to pkg type
	// Priority nil semantics are preserved: nil in CRD → nil in pkg → lowest priority (math.MaxInt)
	var roleMappings []awssts.RoleMapping
	for _, rm := range awsStsSpec.RoleMappings {
		var priority *int
		if rm.Priority != nil {
			p := int(*rm.Priority)
			priority = &p
		}
		roleMappings = append(roleMappings, awssts.RoleMapping{
			RoleArn:  rm.RoleArn,
			Claim:    rm.Claim,
			Matcher:  rm.Matcher,
			Priority: priority,
		})
	}

	// Build AWS STS configuration
	awsStsConfig := &awssts.Config{
		Region:           awsStsSpec.Region,
		Service:          awsStsSpec.Service,
		FallbackRoleArn:  awsStsSpec.FallbackRoleArn,
		RoleMappings:     roleMappings,
		RoleClaim:        awsStsSpec.RoleClaim,
		SessionNameClaim: awsStsSpec.SessionNameClaim,
	}

	// Set session duration if specified
	if awsStsSpec.SessionDuration != nil {
		awsStsConfig.SessionDuration = *awsStsSpec.SessionDuration
	}

	// Use WithAWSStsConfig to add configuration
	// The middleware will be automatically created by PopulateMiddlewareConfigs() in the correct order
	*options = append(*options, runner.WithAWSStsConfig(awsStsConfig))

	return nil
}
