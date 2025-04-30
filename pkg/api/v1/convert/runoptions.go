// Package convert provides conversion functions between API types and internal types.
package convert

import (
	"github.com/StacklokLabs/toolhive/pkg/api/v1"
	"github.com/StacklokLabs/toolhive/pkg/auth"
	"github.com/StacklokLabs/toolhive/pkg/runner"
	"github.com/StacklokLabs/toolhive/pkg/transport/types"
)

// RunOptionsToRunnerConfig converts API v1.RunOptions to a runner.RunConfig.
func RunOptionsToRunnerConfig(opts *v1.RunOptions) *runner.RunConfig {
	if opts == nil {
		return nil
	}

	config := runner.NewRunConfig()

	// Set basic fields
	config.Name = opts.Name
	config.Debug = opts.Debug
	config.TargetHost = opts.TargetHost
	config.K8sPodTemplatePatch = opts.K8sPodTemplatePatch
	config.PermissionProfileNameOrPath = opts.PermissionProfile
	config.Volumes = opts.Volumes
	config.Secrets = opts.Secrets
	config.AuthzConfigPath = opts.AuthzConfig
	config.CmdArgs = opts.CmdArgs

	// Set environment variables
	if opts.EnvVars != nil {
		config.EnvVars = opts.EnvVars
	}

	// Set OIDC config
	if opts.OIDCConfig != nil {
		config.OIDCConfig = &auth.JWTValidatorConfig{
			Issuer:   opts.OIDCConfig.Issuer,
			Audience: opts.OIDCConfig.Audience,
			JWKSURL:  opts.OIDCConfig.JWKSURL,
			ClientID: opts.OIDCConfig.ClientID,
		}
	}

	// Set transport
	if opts.Transport != "" {
		config.Transport = types.TransportType(opts.Transport)
	} else {
		config.Transport = types.TransportTypeStdio
	}

	// Set ports
	if opts.Port > 0 {
		config.Port = opts.Port
	}

	if opts.TargetPort > 0 {
		config.TargetPort = opts.TargetPort
	}

	return config
}

// RunnerConfigToRunOptions converts a runner.RunConfig to API v1.RunOptions.
func RunnerConfigToRunOptions(config *runner.RunConfig) *v1.RunOptions {
	if config == nil {
		return nil
	}

	opts := &v1.RunOptions{
		Name:                config.Name,
		Transport:           v1.TransportType(config.Transport),
		Port:                config.Port,
		TargetPort:          config.TargetPort,
		TargetHost:          config.TargetHost,
		PermissionProfile:   config.PermissionProfileNameOrPath,
		EnvVars:             config.EnvVars,
		Debug:               config.Debug,
		Volumes:             config.Volumes,
		Secrets:             config.Secrets,
		AuthzConfig:         config.AuthzConfigPath,
		CmdArgs:             config.CmdArgs,
		K8sPodTemplatePatch: config.K8sPodTemplatePatch,
	}

	// Set OIDC config
	if config.OIDCConfig != nil {
		opts.OIDCConfig = &v1.OIDCConfig{
			Issuer:   config.OIDCConfig.Issuer,
			Audience: config.OIDCConfig.Audience,
			JWKSURL:  config.OIDCConfig.JWKSURL,
			ClientID: config.OIDCConfig.ClientID,
		}
	}

	return opts
}
