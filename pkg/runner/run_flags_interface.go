package runner

import "time"

// RemoteAuthFlagsInterface defines the interface for remote authentication flags
type RemoteAuthFlagsInterface interface {
	GetEnableRemoteAuth() bool
	GetRemoteAuthClientID() string
	GetRemoteAuthClientSecret() string
	GetRemoteAuthClientSecretFile() string
	GetRemoteAuthScopes() []string
	GetRemoteAuthSkipBrowser() bool
	GetRemoteAuthTimeout() time.Duration
	GetRemoteAuthCallbackPort() int
	GetRemoteAuthIssuer() string
	GetRemoteAuthAuthorizeURL() string
	GetRemoteAuthTokenURL() string
}

// RunFlagsInterface defines the interface for run flags that the PreRunTransformer needs
type RunFlagsInterface interface {
	// Basic configuration
	GetName() string
	GetGroup() string
	GetTransport() string
	GetProxyMode() string
	GetHost() string
	GetProxyPort() int
	GetTargetPort() int
	GetTargetHost() string

	// Environment and volumes
	GetEnv() []string
	GetVolumes() []string
	GetSecrets() []string
	GetEnvFile() string
	GetEnvFileDir() string

	// Security and permissions
	GetPermissionProfile() string
	GetAuthzConfig() string
	GetAuditConfig() string
	GetEnableAudit() bool
	GetCACertPath() string
	GetVerifyImage() string

	// Remote configuration
	GetRemoteURL() string
	GetRemoteAuthFlags() RemoteAuthFlagsInterface
	GetOAuthParams() map[string]string

	// Network and isolation
	GetIsolateNetwork() bool
	GetLabels() []string

	// Tools and filtering
	GetToolsFilter() []string

	// Execution mode
	GetForeground() bool

	// OIDC configuration
	GetThvCABundle() string
	GetJWKSAuthTokenFile() string
	GetJWKSAllowPrivateIP() bool
	GetResourceURL() string

	// Telemetry configuration
	GetOtelEndpoint() string
	GetOtelServiceName() string
	GetOtelSamplingRate() float64
	GetOtelHeaders() []string
	GetOtelInsecure() bool
	GetOtelEnablePrometheusMetricsPath() bool
	GetOtelEnvironmentVariables() []string

	// Kubernetes specific
	GetK8sPodPatch() string

	// Ignore functionality
	GetIgnoreGlobally() bool
	GetPrintOverlays() bool
}
