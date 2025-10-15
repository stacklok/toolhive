# Environment Variable Secrets Fallback Mechanism

## Problem Statement

Currently, ToolHive requires secrets to exist within the configured key manager (encrypted file, 1Password, or none provider) in order to be used by MCP servers. While this approach provides excellent security for interactive use cases, it presents challenges for:

- **CI/CD Systems**: Automated pipelines that cannot interact with keyrings or password managers
- **Container Deployments**: Kubernetes pods and Docker containers where secrets are typically injected as environment variables
- **Automated Workflows**: Scripts and automation tools that need to run without user interaction
- **Cloud Environments**: Serverless functions and cloud services that provide secrets through environment variables

## Goals

- Provide a standalone environment variable secrets provider that can be used as a primary provider
- Create a fallback wrapper that can wrap any provider with environment variable fallback
- Maintain backward compatibility with existing secret management workflows
- Enable seamless operation in CI/CD and containerized environments
- Preserve security by using a clear naming convention that prevents accidental exposure

## Non-Goals

- Replace the existing secrets providers
- Support write operations for environment variable secrets (read-only by nature)
- Support listing of environment variable secrets (for security reasons)

## Architecture Overview

The solution introduces two new components:

1. **Environment Provider**: A standalone provider that reads secrets from environment variables
2. **Fallback Provider**: A wrapper that adds environment variable fallback to any existing provider

```
┌─────────────────────────────────────────────────┐
│                Application Code                  │
└─────────────────┬────────────────────────────────┘
                  │
                  ├─── Option 1: Direct Environment Provider
                  │
                  │    ┌──────────────────────────┐
                  │    │  Environment Provider    │
                  │    │  (Standalone Provider)    │
                  │    │  • Read-only              │
                  │    │  • Cannot list secrets   │
                  │    └──────────────────────────┘
                  │
                  └─── Option 2: Fallback Wrapper
                  
                       ┌──────────────────────────┐
                       │  Fallback Provider       │
                       │  (Wrapper)               │
                       │  • Try primary first     │
                       │  • Fallback to env vars  │
                       └────────┬─────────────────┘
                                │
                       ┌────────┴────────┐
                       ▼                 ▼
              ┌──────────────┐  ┌──────────────┐
              │   Primary    │  │ Environment  │
              │   Provider   │  │   Fallback   │
              │ • Encrypted  │  │ TOOLHIVE_    │
              │ • 1Password  │  │ SECRET_*     │
              │ • None       │  └──────────────┘
              └──────────────┘
```

## Detailed Design

### 1. Environment Variable Naming Convention

Environment variables for secrets will follow a strict naming pattern:
```
TOOLHIVE_SECRET_<secret_name>
```

Where `<secret_name>` is the **exact** secret name as it would be referenced in ToolHive. No transformation or case conversion is performed.

**Examples:**
- Secret `github_token` → `TOOLHIVE_SECRET_github_token`
- Secret `GITHUB_TOKEN` → `TOOLHIVE_SECRET_GITHUB_TOKEN`
- Secret `api-key` → `TOOLHIVE_SECRET_api-key`

### 2. Environment Provider Implementation

Create a new standalone environment provider in `pkg/secrets/environment.go`:

```go
package secrets

import (
    "context"
    "errors"
    "fmt"
    "os"
    "strings"
)

// EnvironmentProvider reads secrets from environment variables
type EnvironmentProvider struct {
    prefix string
}

// NewEnvironmentProvider creates a new environment variable secrets provider
func NewEnvironmentProvider() Provider {
    return &EnvironmentProvider{
        prefix: EnvVarPrefix,
    }
}

// GetSecret retrieves a secret from environment variables
func (e *EnvironmentProvider) GetSecret(_ context.Context, name string) (string, error) {
    if name == "" {
        return "", errors.New("secret name cannot be empty")
    }
    
    envVar := e.prefix + name
    value := os.Getenv(envVar)
    if value == "" {
        return "", fmt.Errorf("secret not found: %s", name)
    }
    
    return value, nil
}

// SetSecret is not supported for environment variables
func (e *EnvironmentProvider) SetSecret(_ context.Context, name, _ string) error {
    if name == "" {
        return errors.New("secret name cannot be empty")
    }
    return errors.New("environment provider is read-only")
}

// DeleteSecret is not supported for environment variables
func (e *EnvironmentProvider) DeleteSecret(_ context.Context, name string) error {
    if name == "" {
        return errors.New("secret name cannot be empty")
    }
    return errors.New("environment provider is read-only")
}

// ListSecrets is not supported for environment variables for security reasons
func (e *EnvironmentProvider) ListSecrets(_ context.Context) ([]SecretDescription, error) {
	return nil, errors.New("environment provider does not support listing secrets for security reasons")
}

// Cleanup is a no-op for environment provider
func (e *EnvironmentProvider) Cleanup() error {
    return nil
}

// Capabilities returns the capabilities of the environment provider
func (e *EnvironmentProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		CanRead:    true,
		CanWrite:   false,
		CanDelete:  false,
		CanList:    false,
		CanCleanup: false,
	}
}
```

### 3. Fallback Provider Implementation

Create a fallback wrapper in `pkg/secrets/fallback.go`:

```go
package secrets

import (
    "context"
    "os"
    "strings"
)

// FallbackProvider wraps a primary provider with environment variable fallback
type FallbackProvider struct {
    primary Provider
    envProvider Provider
}

// NewFallbackProvider creates a new provider with environment variable fallback
func NewFallbackProvider(primary Provider) Provider {
    return &FallbackProvider{
        primary: primary,
        envProvider: &EnvironmentProvider{
            prefix: EnvVarPrefix,
        },
    }
}

// GetSecret attempts to get a secret from the primary provider,
// falling back to environment variables if not found
func (f *FallbackProvider) GetSecret(ctx context.Context, name string) (string, error) {
    // First, try the primary provider
    value, err := f.primary.GetSecret(ctx, name)
    if err == nil {
        return value, nil
    }
    
    // Check if it's a "not found" error
    if !isNotFoundError(err) {
        return "", err
    }
    
    // Try environment variable fallback
    envValue, envErr := f.envProvider.GetSecret(ctx, name)
    if envErr == nil {
        logger.Debugf("Secret '%s' retrieved from environment variable fallback", name)
        return envValue, nil
    }
    
    // Return the original error if no fallback found
    return "", err
}

// SetSecret always uses the primary provider (no env var writes)
func (f *FallbackProvider) SetSecret(ctx context.Context, name, value string) error {
    return f.primary.SetSecret(ctx, name, value)
}

// DeleteSecret always uses the primary provider (no env var deletes)
func (f *FallbackProvider) DeleteSecret(ctx context.Context, name string) error {
    return f.primary.DeleteSecret(ctx, name)
}

// ListSecrets only lists from the primary provider 
// (env vars not listed in fallback mode for security)
func (f *FallbackProvider) ListSecrets(ctx context.Context) ([]SecretDescription, error) {
    return f.primary.ListSecrets(ctx)
}

// Cleanup delegates to the primary provider
func (f *FallbackProvider) Cleanup() error {
    return f.primary.Cleanup()
}

// Capabilities returns the primary provider's capabilities
func (f *FallbackProvider) Capabilities() ProviderCapabilities {
    return f.primary.Capabilities()
}

// isNotFoundError checks if an error indicates a secret was not found
func isNotFoundError(err error) bool {
    if err == nil {
        return false
    }
    errStr := err.Error()
    return strings.Contains(errStr, "not found") || 
           strings.Contains(errStr, "does not exist")
}
```

### 4. Factory Integration

Modify `pkg/secrets/factory.go` to support both the environment provider and fallback wrapper:

```go
// Add new provider type
const (
    // ... existing types ...
    
    // EnvironmentType represents the environment variable secret provider
    EnvironmentType ProviderType = "environment"
)

// CreateSecretProvider creates the specified type of secrets provider
func CreateSecretProvider(managerType ProviderType) (Provider, error) {
    return CreateSecretProviderWithPassword(managerType, "")
}

// CreateSecretProviderWithPassword creates the specified type of secrets provider
func CreateSecretProviderWithPassword(managerType ProviderType, password string) (Provider, error) {
    // Create the primary provider
    var primary Provider
    var err error
    
    switch managerType {
    case EncryptedType:
        // ... existing encrypted provider logic ...
        primary, err = createEncryptedProvider(password)
    case OnePasswordType:
        primary, err = NewOnePasswordManager()
    case NoneType:
        primary, err = NewNoneManager()
    case EnvironmentType:
        // Direct environment provider - no fallback needed
        return NewEnvironmentProvider(), nil
    default:
        return nil, ErrUnknownManagerType
    }
    
    if err != nil {
        return nil, err
    }
    
    // Wrap with fallback provider if enabled
    if shouldEnableFallback() {
        return NewFallbackProvider(primary), nil
    }
    
    return primary, nil
}

// shouldEnableFallback determines if environment variable fallback should be enabled
func shouldEnableFallback() bool {
    // Check for explicit opt-out
    if os.Getenv("TOOLHIVE_DISABLE_ENV_FALLBACK") == "true" {
        return false
    }
    
    // Enable by default for non-environment providers
    return true
}
```

## Security Considerations

### 1. Namespace Isolation
The `TOOLHIVE_SECRET_` prefix ensures clear separation from other environment variables.

### 2. Read-Only Nature
Environment variables are inherently read-only in the provider, preventing accidental modifications.

### 3. Audit Logging
When a secret is retrieved from environment variables (either directly or via fallback), this is logged at debug level.

### 4. No Listing Support
Environment variable secrets do not support listing operations to prevent enumeration and accidental exposure of secret names in logs or output.

## Use Cases

### 1. GitHub Actions (StacklokLabs/toolhive-actions)

The [toolhive-actions](https://github.com/StacklokLabs/toolhive-actions) repository currently has TODOs for secrets support. With this implementation, the action can be updated to support secrets:

```yaml
# In the run-mcp-server action.yml, the TODO for secrets can be resolved:
- name: Run GitHub MCP Server
  uses: stackloklabs/toolhive-actions/run-mcp-server@v0
  env:
    # Secrets are passed as environment variables with the TOOLHIVE_SECRET_ prefix
    TOOLHIVE_SECRET_github_token: ${{ secrets.GITHUB_TOKEN }}
    TOOLHIVE_SECRET_npm_token: ${{ secrets.NPM_TOKEN }}
  with:
    server: github
    # The action can now use these secrets without complex setup
```

The action's build command step can be updated to reference secrets:
```bash
# In the action implementation
if [ -n "${{ inputs.secrets }}" ]; then
  # Parse JSON secrets and set as environment variables
  echo "${{ inputs.secrets }}" | jq -r 'to_entries[] | "TOOLHIVE_SECRET_\(.key)=\(.value)"' >> $GITHUB_ENV
fi

# Then run with secrets available via environment fallback
thv run --secret github_token,target=GITHUB_TOKEN github
```

### 2. Kubernetes Operator (thv-operator)

The ToolHive operator already has support for secrets via the `MCPServerPodTemplateSpecBuilder`. With the environment variable fallback, secrets can be injected using the `TOOLHIVE_SECRET_` prefix:

```yaml
apiVersion: toolhive.stacklok.io/v1alpha1
kind: MCPServer
metadata:
  name: github-server
spec:
  server: github
  # The operator's WithSecrets method already handles secret injection
  secrets:
    - name: github-credentials
      key: token
      targetEnvName: TOOLHIVE_SECRET_github_token  # Uses our prefix
    - name: api-credentials
      key: key
      targetEnvName: TOOLHIVE_SECRET_api_key
```

The operator's `WithSecrets` method in `mcpserver_podtemplatespec_builder.go` already creates the appropriate environment variables:
```go
// From the operator implementation
secretEnvVars = append(secretEnvVars, corev1.EnvVar{
    Name: targetEnv,  // This would be TOOLHIVE_SECRET_github_token
    ValueFrom: &corev1.EnvVarSource{
        SecretKeyRef: &corev1.SecretKeySelector{
            LocalObjectReference: corev1.LocalObjectReference{
                Name: secret.Name,
            },
            Key: secret.Key,
        },
    },
})
```

### 3. Local Development with Fallback

```bash
# Use existing provider with fallback for temporary secrets
export TOOLHIVE_SECRETS_PROVIDER=encrypted  # or 1password

# Add temporary secrets via environment
export TOOLHIVE_SECRET_dev_token="dev-token-12345"
export TOOLHIVE_SECRET_test_api_key="test-key-67890"

# Permanent secrets from keyring, temporary from environment
thv run --secret api_key,target=API_KEY my-server
```

### 4. CI/CD with Direct Environment Provider

```yaml
# GitHub Actions with explicit environment provider
- name: Setup ToolHive for CI
  run: |
    # Use environment provider directly in CI
    export TOOLHIVE_SECRETS_PROVIDER=environment
    export TOOLHIVE_SECRET_github_token=${{ secrets.GITHUB_TOKEN }}
    export TOOLHIVE_SECRET_npm_token=${{ secrets.NPM_TOKEN }}
    
    # All secrets come from environment variables
    thv run --secret github_token,target=GITHUB_TOKEN github
```

### 5. Production with Disabled Fallback

```bash
# Strict production setup - no fallback
export TOOLHIVE_DISABLE_ENV_FALLBACK=true
export TOOLHIVE_SECRETS_PROVIDER=1password

# Will only use 1Password, no environment fallback
# This ensures all secrets come from the secure provider
thv run --secret api_key,target=API_KEY my-server
```

## Conclusion

This modular design provides maximum flexibility by offering both a standalone environment provider and a fallback wrapper. Organizations can choose the approach that best fits their security requirements:

- **Environment Provider**: Simple, direct access to environment variables for CI/CD
- **Fallback Wrapper**: Enhanced existing providers with environment variable fallback for mixed environments

The implementation maintains backward compatibility while enabling new deployment scenarios, particularly in automated and containerized environments where traditional secret management is challenging.