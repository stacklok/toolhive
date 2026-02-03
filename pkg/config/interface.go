// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// Provider defines the interface for configuration operations
//
//go:generate mockgen -destination=mocks/mock_provider.go -package=mocks -source=interface.go Provider
type Provider interface {
	GetConfig() *Config
	UpdateConfig(updateFn func(*Config)) error
	LoadOrCreateConfig() (*Config, error)

	// Registry operations
	SetRegistryURL(registryURL string, allowPrivateRegistryIp bool) error
	SetRegistryAPI(apiURL string, allowPrivateRegistryIp bool) error
	SetRegistryFile(registryPath string) error
	UnsetRegistry() error
	GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string)

	// CA certificate operations
	SetCACert(certPath string) error
	GetCACert() (certPath string, exists bool, accessible bool)
	UnsetCACert() error

	// Build environment operations
	SetBuildEnv(key, value string) error
	GetBuildEnv(key string) (value string, exists bool)
	GetAllBuildEnv() map[string]string
	UnsetBuildEnv(key string) error
	UnsetAllBuildEnv() error

	// Build environment from secrets operations
	SetBuildEnvFromSecret(key, secretName string) error
	GetBuildEnvFromSecret(key string) (secretName string, exists bool)
	GetAllBuildEnvFromSecrets() map[string]string
	UnsetBuildEnvFromSecret(key string) error

	// Build environment from shell operations
	SetBuildEnvFromShell(key string) error
	GetBuildEnvFromShell(key string) (exists bool)
	GetAllBuildEnvFromShell() []string
	UnsetBuildEnvFromShell(key string) error

	// Build auth file operations (content stored in secrets provider, not config)
	MarkBuildAuthFileConfigured(name string) error
	IsBuildAuthFileConfigured(name string) bool
	GetConfiguredBuildAuthFiles() []string
	UnsetBuildAuthFile(name string) error
	UnsetAllBuildAuthFiles() error
}

// DefaultProvider implements Provider using the default XDG config path
type DefaultProvider struct{}

// NewDefaultProvider creates a new default config provider
func NewDefaultProvider() *DefaultProvider {
	return &DefaultProvider{}
}

// GetConfig returns the singleton config (for backward compatibility)
func (*DefaultProvider) GetConfig() *Config {
	return getSingletonConfig()
}

// UpdateConfig updates the config using the default path
func (*DefaultProvider) UpdateConfig(updateFn func(*Config)) error {
	return UpdateConfigAtPath("", updateFn)
}

// LoadOrCreateConfig loads or creates config using the default path
func (*DefaultProvider) LoadOrCreateConfig() (*Config, error) {
	return LoadOrCreateConfigWithDefaultPath()
}

// SetRegistryURL validates and sets a registry URL
func (d *DefaultProvider) SetRegistryURL(registryURL string, allowPrivateRegistryIp bool) error {
	return setRegistryURL(d, registryURL, allowPrivateRegistryIp)
}

// SetRegistryAPI validates and sets an MCP Registry API endpoint
func (d *DefaultProvider) SetRegistryAPI(apiURL string, allowPrivateRegistryIp bool) error {
	return setRegistryAPI(d, apiURL, allowPrivateRegistryIp)
}

// SetRegistryFile validates and sets a local registry file
func (d *DefaultProvider) SetRegistryFile(registryPath string) error {
	return setRegistryFile(d, registryPath)
}

// UnsetRegistry resets registry configuration to defaults
func (d *DefaultProvider) UnsetRegistry() error {
	return unsetRegistry(d)
}

// GetRegistryConfig returns current registry configuration
func (d *DefaultProvider) GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string) {
	return getRegistryConfig(d)
}

// SetCACert validates and sets the CA certificate path
func (d *DefaultProvider) SetCACert(certPath string) error {
	return setCACert(d, certPath)
}

// GetCACert returns the currently configured CA certificate path and its accessibility status
func (d *DefaultProvider) GetCACert() (certPath string, exists bool, accessible bool) {
	return getCACert(d)
}

// UnsetCACert removes the CA certificate configuration
func (d *DefaultProvider) UnsetCACert() error {
	return unsetCACert(d)
}

// SetBuildEnv validates and sets a build environment variable
func (d *DefaultProvider) SetBuildEnv(key, value string) error {
	return setBuildEnv(d, key, value)
}

// GetBuildEnv returns a specific build environment variable
func (d *DefaultProvider) GetBuildEnv(key string) (value string, exists bool) {
	return getBuildEnv(d, key)
}

// GetAllBuildEnv returns all build environment variables
func (d *DefaultProvider) GetAllBuildEnv() map[string]string {
	return getAllBuildEnv(d)
}

// UnsetBuildEnv removes a specific build environment variable
func (d *DefaultProvider) UnsetBuildEnv(key string) error {
	return unsetBuildEnv(d, key)
}

// UnsetAllBuildEnv removes all build environment variables
func (d *DefaultProvider) UnsetAllBuildEnv() error {
	return unsetAllBuildEnv(d)
}

// SetBuildEnvFromSecret validates and sets a secret reference for a build environment variable
func (d *DefaultProvider) SetBuildEnvFromSecret(key, secretName string) error {
	return setBuildEnvFromSecret(d, key, secretName)
}

// GetBuildEnvFromSecret retrieves the secret name for a build environment variable
func (d *DefaultProvider) GetBuildEnvFromSecret(key string) (secretName string, exists bool) {
	return getBuildEnvFromSecret(d, key)
}

// GetAllBuildEnvFromSecrets returns all build env secret references
func (d *DefaultProvider) GetAllBuildEnvFromSecrets() map[string]string {
	return getAllBuildEnvFromSecrets(d)
}

// UnsetBuildEnvFromSecret removes a secret reference
func (d *DefaultProvider) UnsetBuildEnvFromSecret(key string) error {
	return unsetBuildEnvFromSecret(d, key)
}

// SetBuildEnvFromShell adds an environment variable name to read from shell at build time
func (d *DefaultProvider) SetBuildEnvFromShell(key string) error {
	return setBuildEnvFromShell(d, key)
}

// GetBuildEnvFromShell checks if a key is configured to read from shell
func (d *DefaultProvider) GetBuildEnvFromShell(key string) bool {
	return getBuildEnvFromShell(d, key)
}

// GetAllBuildEnvFromShell returns all keys configured to read from shell
func (d *DefaultProvider) GetAllBuildEnvFromShell() []string {
	return getAllBuildEnvFromShell(d)
}

// UnsetBuildEnvFromShell removes a key from shell environment list
func (d *DefaultProvider) UnsetBuildEnvFromShell(key string) error {
	return unsetBuildEnvFromShell(d, key)
}

// MarkBuildAuthFileConfigured marks an auth file type as configured
func (d *DefaultProvider) MarkBuildAuthFileConfigured(name string) error {
	return markBuildAuthFileConfigured(d, name)
}

// IsBuildAuthFileConfigured checks if an auth file type is configured
func (d *DefaultProvider) IsBuildAuthFileConfigured(name string) bool {
	return isBuildAuthFileConfigured(d, name)
}

// GetConfiguredBuildAuthFiles returns list of configured auth file types
func (d *DefaultProvider) GetConfiguredBuildAuthFiles() []string {
	return getConfiguredBuildAuthFiles(d)
}

// UnsetBuildAuthFile removes an auth file configuration
func (d *DefaultProvider) UnsetBuildAuthFile(name string) error {
	return unsetBuildAuthFile(d, name)
}

// UnsetAllBuildAuthFiles removes all auth file configurations
func (d *DefaultProvider) UnsetAllBuildAuthFiles() error {
	return unsetAllBuildAuthFiles(d)
}

// PathProvider implements Provider using a specific config path
type PathProvider struct {
	configPath string
}

// NewPathProvider creates a new config provider with a specific path
func NewPathProvider(configPath string) *PathProvider {
	return &PathProvider{configPath: configPath}
}

// GetConfig loads and returns the config from the specific path
func (p *PathProvider) GetConfig() *Config {
	config, err := LoadOrCreateConfigWithPath(p.configPath)
	if err != nil {
		// Return default config on error, similar to singleton behavior
		defaultConfig := createNewConfigWithDefaults()
		return &defaultConfig
	}
	return config
}

// UpdateConfig updates the config at the specific path
func (p *PathProvider) UpdateConfig(updateFn func(*Config)) error {
	return UpdateConfigAtPath(p.configPath, updateFn)
}

// LoadOrCreateConfig loads or creates config at the specific path
func (p *PathProvider) LoadOrCreateConfig() (*Config, error) {
	return LoadOrCreateConfigWithPath(p.configPath)
}

// SetRegistryURL validates and sets a registry URL
func (p *PathProvider) SetRegistryURL(registryURL string, allowPrivateRegistryIp bool) error {
	return setRegistryURL(p, registryURL, allowPrivateRegistryIp)
}

// SetRegistryAPI validates and sets an MCP Registry API endpoint
func (p *PathProvider) SetRegistryAPI(apiURL string, allowPrivateRegistryIp bool) error {
	return setRegistryAPI(p, apiURL, allowPrivateRegistryIp)
}

// SetRegistryFile validates and sets a local registry file
func (p *PathProvider) SetRegistryFile(registryPath string) error {
	return setRegistryFile(p, registryPath)
}

// UnsetRegistry resets registry configuration to defaults
func (p *PathProvider) UnsetRegistry() error {
	return unsetRegistry(p)
}

// GetRegistryConfig returns current registry configuration
func (p *PathProvider) GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string) {
	return getRegistryConfig(p)
}

// SetCACert validates and sets the CA certificate path
func (p *PathProvider) SetCACert(certPath string) error {
	return setCACert(p, certPath)
}

// GetCACert returns the currently configured CA certificate path and its accessibility status
func (p *PathProvider) GetCACert() (certPath string, exists bool, accessible bool) {
	return getCACert(p)
}

// UnsetCACert removes the CA certificate configuration
func (p *PathProvider) UnsetCACert() error {
	return unsetCACert(p)
}

// SetBuildEnv validates and sets a build environment variable
func (p *PathProvider) SetBuildEnv(key, value string) error {
	return setBuildEnv(p, key, value)
}

// GetBuildEnv returns a specific build environment variable
func (p *PathProvider) GetBuildEnv(key string) (value string, exists bool) {
	return getBuildEnv(p, key)
}

// GetAllBuildEnv returns all build environment variables
func (p *PathProvider) GetAllBuildEnv() map[string]string {
	return getAllBuildEnv(p)
}

// UnsetBuildEnv removes a specific build environment variable
func (p *PathProvider) UnsetBuildEnv(key string) error {
	return unsetBuildEnv(p, key)
}

// UnsetAllBuildEnv removes all build environment variables
func (p *PathProvider) UnsetAllBuildEnv() error {
	return unsetAllBuildEnv(p)
}

// SetBuildEnvFromSecret validates and sets a secret reference for a build environment variable
func (p *PathProvider) SetBuildEnvFromSecret(key, secretName string) error {
	return setBuildEnvFromSecret(p, key, secretName)
}

// GetBuildEnvFromSecret retrieves the secret name for a build environment variable
func (p *PathProvider) GetBuildEnvFromSecret(key string) (secretName string, exists bool) {
	return getBuildEnvFromSecret(p, key)
}

// GetAllBuildEnvFromSecrets returns all build env secret references
func (p *PathProvider) GetAllBuildEnvFromSecrets() map[string]string {
	return getAllBuildEnvFromSecrets(p)
}

// UnsetBuildEnvFromSecret removes a secret reference
func (p *PathProvider) UnsetBuildEnvFromSecret(key string) error {
	return unsetBuildEnvFromSecret(p, key)
}

// SetBuildEnvFromShell adds an environment variable name to read from shell at build time
func (p *PathProvider) SetBuildEnvFromShell(key string) error {
	return setBuildEnvFromShell(p, key)
}

// GetBuildEnvFromShell checks if a key is configured to read from shell
func (p *PathProvider) GetBuildEnvFromShell(key string) bool {
	return getBuildEnvFromShell(p, key)
}

// GetAllBuildEnvFromShell returns all keys configured to read from shell
func (p *PathProvider) GetAllBuildEnvFromShell() []string {
	return getAllBuildEnvFromShell(p)
}

// UnsetBuildEnvFromShell removes a key from shell environment list
func (p *PathProvider) UnsetBuildEnvFromShell(key string) error {
	return unsetBuildEnvFromShell(p, key)
}

// MarkBuildAuthFileConfigured marks an auth file type as configured
func (p *PathProvider) MarkBuildAuthFileConfigured(name string) error {
	return markBuildAuthFileConfigured(p, name)
}

// IsBuildAuthFileConfigured checks if an auth file type is configured
func (p *PathProvider) IsBuildAuthFileConfigured(name string) bool {
	return isBuildAuthFileConfigured(p, name)
}

// GetConfiguredBuildAuthFiles returns list of configured auth file types
func (p *PathProvider) GetConfiguredBuildAuthFiles() []string {
	return getConfiguredBuildAuthFiles(p)
}

// UnsetBuildAuthFile removes an auth file configuration
func (p *PathProvider) UnsetBuildAuthFile(name string) error {
	return unsetBuildAuthFile(p, name)
}

// UnsetAllBuildAuthFiles removes all auth file configurations
func (p *PathProvider) UnsetAllBuildAuthFiles() error {
	return unsetAllBuildAuthFiles(p)
}

// KubernetesProvider is a no-op implementation of Provider for Kubernetes environments.
// In Kubernetes, configuration is managed by the cluster, not by local files.
type KubernetesProvider struct{}

// NewKubernetesProvider creates a new no-op config provider for Kubernetes environments
func NewKubernetesProvider() *KubernetesProvider {
	return &KubernetesProvider{}
}

// GetConfig returns a default config for Kubernetes environments
func (*KubernetesProvider) GetConfig() *Config {
	config := createNewConfigWithDefaults()
	return &config
}

// UpdateConfig is a no-op for Kubernetes environments
func (*KubernetesProvider) UpdateConfig(_ func(*Config)) error {
	return nil
}

// LoadOrCreateConfig returns a default config for Kubernetes environments
func (*KubernetesProvider) LoadOrCreateConfig() (*Config, error) {
	config := createNewConfigWithDefaults()
	return &config, nil
}

// SetRegistryURL is a no-op for Kubernetes environments
func (*KubernetesProvider) SetRegistryURL(_ string, _ bool) error {
	return nil
}

// SetRegistryAPI is a no-op for Kubernetes environments
func (*KubernetesProvider) SetRegistryAPI(_ string, _ bool) error {
	return nil
}

// SetRegistryFile is a no-op for Kubernetes environments
func (*KubernetesProvider) SetRegistryFile(_ string) error {
	return nil
}

// UnsetRegistry is a no-op for Kubernetes environments
func (*KubernetesProvider) UnsetRegistry() error {
	return nil
}

// GetRegistryConfig returns empty registry configuration for Kubernetes environments
func (*KubernetesProvider) GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string) {
	return "", "", false, ""
}

// SetCACert is a no-op for Kubernetes environments
func (*KubernetesProvider) SetCACert(_ string) error {
	return nil
}

// GetCACert returns empty CA cert configuration for Kubernetes environments
func (*KubernetesProvider) GetCACert() (certPath string, exists bool, accessible bool) {
	return "", false, false
}

// UnsetCACert is a no-op for Kubernetes environments
func (*KubernetesProvider) UnsetCACert() error {
	return nil
}

// SetBuildEnv is a no-op for Kubernetes environments
func (*KubernetesProvider) SetBuildEnv(_, _ string) error {
	return nil
}

// GetBuildEnv returns empty for Kubernetes environments
func (*KubernetesProvider) GetBuildEnv(_ string) (value string, exists bool) {
	return "", false
}

// GetAllBuildEnv returns empty map for Kubernetes environments
func (*KubernetesProvider) GetAllBuildEnv() map[string]string {
	return make(map[string]string)
}

// UnsetBuildEnv is a no-op for Kubernetes environments
func (*KubernetesProvider) UnsetBuildEnv(_ string) error {
	return nil
}

// UnsetAllBuildEnv is a no-op for Kubernetes environments
func (*KubernetesProvider) UnsetAllBuildEnv() error {
	return nil
}

// SetBuildEnvFromSecret is a no-op for Kubernetes environments
func (*KubernetesProvider) SetBuildEnvFromSecret(_, _ string) error {
	return nil
}

// GetBuildEnvFromSecret returns empty for Kubernetes environments
func (*KubernetesProvider) GetBuildEnvFromSecret(_ string) (secretName string, exists bool) {
	return "", false
}

// GetAllBuildEnvFromSecrets returns empty map for Kubernetes environments
func (*KubernetesProvider) GetAllBuildEnvFromSecrets() map[string]string {
	return make(map[string]string)
}

// UnsetBuildEnvFromSecret is a no-op for Kubernetes environments
func (*KubernetesProvider) UnsetBuildEnvFromSecret(_ string) error {
	return nil
}

// SetBuildEnvFromShell is a no-op for Kubernetes environments
func (*KubernetesProvider) SetBuildEnvFromShell(_ string) error {
	return nil
}

// GetBuildEnvFromShell returns false for Kubernetes environments
func (*KubernetesProvider) GetBuildEnvFromShell(_ string) bool {
	return false
}

// GetAllBuildEnvFromShell returns empty slice for Kubernetes environments
func (*KubernetesProvider) GetAllBuildEnvFromShell() []string {
	return []string{}
}

// UnsetBuildEnvFromShell is a no-op for Kubernetes environments
func (*KubernetesProvider) UnsetBuildEnvFromShell(_ string) error {
	return nil
}

// MarkBuildAuthFileConfigured is a no-op for Kubernetes environments
func (*KubernetesProvider) MarkBuildAuthFileConfigured(_ string) error {
	return nil
}

// IsBuildAuthFileConfigured returns false for Kubernetes environments
func (*KubernetesProvider) IsBuildAuthFileConfigured(_ string) bool {
	return false
}

// GetConfiguredBuildAuthFiles returns empty slice for Kubernetes environments
func (*KubernetesProvider) GetConfiguredBuildAuthFiles() []string {
	return []string{}
}

// UnsetBuildAuthFile is a no-op for Kubernetes environments
func (*KubernetesProvider) UnsetBuildAuthFile(_ string) error {
	return nil
}

// UnsetAllBuildAuthFiles is a no-op for Kubernetes environments
func (*KubernetesProvider) UnsetAllBuildAuthFiles() error {
	return nil
}

// NewProvider creates the appropriate config provider based on the runtime environment
func NewProvider() Provider {
	if runtime.IsKubernetesRuntime() {
		return NewKubernetesProvider()
	}
	return NewDefaultProvider()
}
