package controllerutil

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/pkg/runner"
)

// AddOIDCConfigOptions adds OIDC configuration options to builder options
func AddOIDCConfigOptions(
	ctx context.Context,
	c client.Client,
	res oidc.OIDCConfigurable,
	options *[]runner.RunConfigBuilderOption,
) error {
	// Use the OIDC resolver to get configuration
	resolver := oidc.NewResolver(c)
	oidcConfig, err := resolver.Resolve(ctx, res)
	if err != nil {
		return fmt.Errorf("failed to resolve OIDC configuration: %w", err)
	}

	if oidcConfig == nil {
		return nil
	}

	// Add OIDC config to options
	*options = append(*options, runner.WithOIDCConfig(
		oidcConfig.Issuer,
		oidcConfig.Audience,
		oidcConfig.JWKSURL,
		oidcConfig.IntrospectionURL,
		oidcConfig.ClientID,
		oidcConfig.ClientSecret,
		oidcConfig.ThvCABundlePath,
		oidcConfig.JWKSAuthTokenPath,
		oidcConfig.ResourceURL,
		oidcConfig.JWKSAllowPrivateIP,
		oidcConfig.InsecureAllowHTTP,
	))

	return nil
}
