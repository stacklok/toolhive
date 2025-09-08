# Generalized Template Customization System for ToolHive

## Issue Reference
GitHub Issue: [#1737](https://github.com/stacklok/toolhive/issues/1737) - Support for private NPM registries

## Executive Summary
This proposal presents a generalized, extensible system for customizing Docker build templates in ToolHive. While the immediate need is NPM private registry support, this design enables configuration of any package manager (npm, pip/uv, go) and supports future extensibility without code changes.

## Problem Statement

### Current Limitations
1. No support for private package registries
2. Hard-coded package manager configurations
3. No flexibility for enterprise environments with custom requirements
4. Each new configuration need requires code changes

### Use Cases
- Private NPM registries for enterprise packages
- Python packages from private PyPI servers
- Go modules behind corporate proxies
- Custom authentication mechanisms
- Environment-specific build configurations
- Compliance requirements (specific mirrors, audit logs)

## Design Principles

1. **Extensibility**: Support new configurations without code changes
2. **Stage-Awareness**: Respect multi-stage Docker build patterns
3. **Security**: Keep secrets in builder stage only
4. **Flexibility**: Support multiple configuration methods
5. **Backward Compatibility**: Don't break existing functionality
6. **Simplicity**: Progressive complexity - simple things simple, complex things possible

## Proposed Solution: Stage-Aware Configuration System

### Core Architecture

#### 1. Enhanced Template Data Model

```go
// pkg/container/templates/templates.go

type TemplateData struct {
    // Existing fields
    MCPPackage    string
    MCPArgs       []string
    CACertContent string
    IsLocalPath   bool
    
    // New: Stage-aware configuration system
    Stages StagesConfig `json:"stages,omitempty" yaml:"stages,omitempty"`
}

type StagesConfig struct {
    Builder StageConfig `json:"builder,omitempty" yaml:"builder,omitempty"`
    Runtime StageConfig `json:"runtime,omitempty" yaml:"runtime,omitempty"`
}

type StageConfig struct {
    // Environment variables for this stage
    Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
    
    // Package manager configuration commands
    // Executed BEFORE package installation
    Setup []Command `json:"setup,omitempty" yaml:"setup,omitempty"`
    
    // Pre-install hooks
    PreInstall []Command `json:"pre_install,omitempty" yaml:"pre_install,omitempty"`
    
    // Post-install hooks
    PostInstall []Command `json:"post_install,omitempty" yaml:"post_install,omitempty"`
    
    // Files to copy into the stage
    Files []FileMount `json:"files,omitempty" yaml:"files,omitempty"`
}

type Command struct {
    Run   string            `json:"run" yaml:"run"`
    When  *WhenCondition    `json:"when,omitempty" yaml:"when,omitempty"`
    Env   map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
}

type WhenCondition struct {
    // Condition based on template variables
    If string `json:"if,omitempty" yaml:"if,omitempty"`
}

type FileMount struct {
    Source string `json:"source" yaml:"source"`
    Dest   string `json:"dest" yaml:"dest"`
    Mode   string `json:"mode,omitempty" yaml:"mode,omitempty"`
}
```

#### 2. Configuration Profiles System

```go
// pkg/container/templates/profiles.go

type Profile struct {
    Name        string                 `json:"name" yaml:"name"`
    Description string                 `json:"description,omitempty" yaml:"description,omitempty"`
    Extends     []string              `json:"extends,omitempty" yaml:"extends,omitempty"`
    Applies     AppliesCondition      `json:"applies,omitempty" yaml:"applies,omitempty"`
    Stages      StagesConfig          `json:"stages" yaml:"stages"`
    Variables   map[string]string     `json:"variables,omitempty" yaml:"variables,omitempty"`
}

type AppliesCondition struct {
    // Auto-apply based on package patterns
    PackagePattern string   `json:"package_pattern,omitempty" yaml:"package_pattern,omitempty"`
    Transport      []string `json:"transport,omitempty" yaml:"transport,omitempty"`
}

// Built-in profiles
var BuiltinProfiles = map[string]Profile{
    "npm-private": {
        Name: "npm-private",
        Description: "Configure NPM to use a private registry",
        Applies: AppliesCondition{
            Transport: []string{"npx"},
        },
        Variables: map[string]string{
            "NPM_REGISTRY": "${NPM_REGISTRY_URL}",
        },
        Stages: StagesConfig{
            Builder: StageConfig{
                Env: map[string]string{
                    "NPM_CONFIG_REGISTRY": "${NPM_REGISTRY}",
                },
                Setup: []Command{
                    {Run: "npm config set registry ${NPM_REGISTRY}"},
                    {
                        Run: "npm config set //${NPM_REGISTRY#https://}/:_authToken ${NPM_TOKEN}",
                        When: &WhenCondition{If: "NPM_TOKEN"},
                    },
                },
            },
        },
    },
    "pypi-private": {
        Name: "pypi-private",
        Description: "Configure pip/uv to use a private PyPI index",
        Applies: AppliesCondition{
            Transport: []string{"uvx"},
        },
        Variables: map[string]string{
            "PYPI_INDEX": "${PYPI_INDEX_URL}",
        },
        Stages: StagesConfig{
            Builder: StageConfig{
                Env: map[string]string{
                    "PIP_INDEX_URL": "${PYPI_INDEX}",
                    "UV_INDEX_URL": "${PYPI_INDEX}",
                },
                Setup: []Command{
                    {
                        Run: "pip config set global.trusted-host ${PYPI_TRUSTED_HOST}",
                        When: &WhenCondition{If: "PYPI_TRUSTED_HOST"},
                    },
                },
            },
        },
    },
    "go-corporate": {
        Name: "go-corporate",
        Description: "Configure Go modules for corporate environment",
        Applies: AppliesCondition{
            Transport: []string{"go"},
        },
        Stages: StagesConfig{
            Builder: StageConfig{
                Env: map[string]string{
                    "GOPROXY": "${GO_PROXY}",
                    "GOPRIVATE": "${GO_PRIVATE}",
                    "GONOSUMDB": "${GO_PRIVATE}",
                },
                Setup: []Command{
                    {
                        Run: "git config --global url.\"https://${GIT_TOKEN}@github.com/\".insteadOf \"https://github.com/\"",
                        When: &WhenCondition{If: "GIT_TOKEN"},
                    },
                },
            },
        },
    },
}
```

#### 3. Template Engine Integration

```go
// pkg/container/templates/engine.go

type TemplateEngine struct {
    profiles map[string]Profile
    funcs    template.FuncMap
}

func NewTemplateEngine() *TemplateEngine {
    return &TemplateEngine{
        profiles: make(map[string]Profile),
        funcs: template.FuncMap{
            "evalCondition": evalCondition,
            "expandVars": expandVars,
        },
    }
}

func (e *TemplateEngine) RenderTemplate(
    templateType TransportType,
    data TemplateData,
    profiles []string,
    variables map[string]string,
) (string, error) {
    // Merge profiles into template data
    mergedData := e.mergeProfiles(data, profiles, variables)
    
    // Load and execute template
    tmpl, err := e.loadTemplate(templateType)
    if err != nil {
        return "", err
    }
    
    var buf bytes.Buffer
    if err := tmpl.Execute(&buf, mergedData); err != nil {
        return "", err
    }
    
    return buf.String(), nil
}
```

### Updated Template Structure

```dockerfile
# pkg/container/templates/npx.tmpl
FROM node:22-alpine AS builder

{{/* CA Certificate handling remains the same */}}
{{if .CACertContent}}
# Add custom CA certificate BEFORE any network operations
COPY ca-cert.crt /tmp/custom-ca.crt
RUN cat /tmp/custom-ca.crt >> /etc/ssl/certs/ca-certificates.crt && \
    rm /tmp/custom-ca.crt
{{end}}

# Install system dependencies
RUN apk add --no-cache git ca-certificates

{{/* Apply builder stage environment variables */}}
{{range $key, $value := .Stages.Builder.Env}}
ENV {{$key}}="{{$value}}"
{{end}}

{{/* Copy any required files for this stage */}}
{{range .Stages.Builder.Files}}
COPY {{.Source}} {{.Dest}}
{{if .Mode}}RUN chmod {{.Mode}} {{.Dest}}{{end}}
{{end}}

{{/* Run setup commands (package manager configuration) */}}
{{range .Stages.Builder.Setup}}
{{if not .When}}
RUN {{.Run}}
{{else if evalCondition .When.If}}
RUN {{.Run}}
{{end}}
{{end}}

# Set working directory
WORKDIR /build

{{/* Run pre-install hooks */}}
{{range .Stages.Builder.PreInstall}}
RUN {{.Run}}
{{end}}

{{/* Package installation - unchanged */}}
{{if .IsLocalPath}}
COPY . /build/
RUN if [ -f package.json ]; then npm ci --only=production || npm install --production; fi
{{else}}
RUN echo '{"name":"mcp-container","version":"1.0.0"}' > package.json
RUN npm install --save {{.MCPPackage}}
{{end}}

{{/* Run post-install hooks */}}
{{range .Stages.Builder.PostInstall}}
RUN {{.Run}}
{{end}}

# Runtime stage configuration follows similar pattern...
FROM node:22-alpine
# ... (runtime stage with .Stages.Runtime configuration)
```

### Configuration Methods

#### 1. CLI Flags with Profiles

Both `thv build` and `thv run` commands support the same configuration options:

##### Build Command
```bash
# Build with a profile
thv build npx://@company/tool --profile npm-private \
  --var NPM_REGISTRY_URL=https://npm.company.com \
  --tag company/tool:latest

# Build with inline configuration
thv build npx://@company/tool \
  --stage-env builder:NPM_CONFIG_REGISTRY=https://npm.company.com \
  --stage-setup builder:"npm config set registry https://npm.company.com" \
  --tag company/tool:latest

# Generate Dockerfile with configuration (dry-run)
thv build npx://@company/tool --profile npm-private \
  --var NPM_REGISTRY_URL=https://npm.company.com \
  --dry-run > Dockerfile

# Build with configuration file
thv build npx://@company/tool \
  --config-file .toolhive/config.yaml \
  --tag company/tool:latest
```

##### Run Command
```bash
# Run with a built-in profile
thv run npx://@company/tool --profile npm-private \
  --var NPM_REGISTRY_URL=https://npm.company.com \
  --var NPM_TOKEN=$NPM_TOKEN

# Multiple profiles
thv run npx://@company/tool \
  --profile npm-private \
  --profile corporate-ca \
  --var NPM_REGISTRY_URL=https://npm.company.com

# Inline configuration
thv run npx://@company/tool \
  --stage-env builder:NPM_CONFIG_REGISTRY=https://npm.company.com \
  --stage-setup builder:"npm config set registry https://npm.company.com"
```

#### 2. Configuration Files

```yaml
# .toolhive/config.yaml
profiles:
  - npm-private
  - corporate-security

variables:
  NPM_REGISTRY_URL: https://npm.company.com
  NPM_TOKEN: ${ENV:NPM_AUTH_TOKEN}

stages:
  builder:
    env:
      NODE_ENV: production
      NPM_CONFIG_LOGLEVEL: verbose
    setup:
      - run: npm config set registry ${NPM_REGISTRY_URL}
      - run: npm config set strict-ssl false
        when:
          if: INSECURE_REGISTRY
    pre_install:
      - run: npm audit --audit-level=moderate
```

#### 3. Project-Level Configuration

```yaml
# .toolhive/project.yaml
defaults:
  profiles:
    - company-standard
  
  variables:
    NPM_REGISTRY_URL: https://npm.company.com

overrides:
  # Override for specific packages
  "@company/*":
    profiles:
      - npm-private
      - strict-security
    
  "uvx://*":
    stages:
      builder:
        env:
          PIP_INDEX_URL: https://pypi.company.com/simple
```

### Command Integration

#### RunConfig Integration
```go
// pkg/runner/config.go
type RunConfig struct {
    // ... existing fields ...
    
    // New configuration system
    Profiles  []string               `json:"profiles,omitempty" yaml:"profiles,omitempty"`
    Variables map[string]string      `json:"variables,omitempty" yaml:"variables,omitempty"`
    Stages    StagesConfig          `json:"stages,omitempty" yaml:"stages,omitempty"`
    
    // Path to configuration file
    ConfigFile string `json:"config_file,omitempty" yaml:"config_file,omitempty"`
}
```

#### BuildFlags Integration
```go
// cmd/thv/app/build.go
type BuildFlags struct {
    Tag    string
    Output string
    DryRun bool
    
    // New configuration fields
    Profiles   []string
    Variables  map[string]string
    ConfigFile string
    StageEnv   []string  // Format: "stage:KEY=VALUE"
    StageSetup []string  // Format: "stage:command"
}
```

#### Protocol Handler Updates
```go
// pkg/runner/protocol.go
func BuildFromProtocolSchemeWithName(
    ctx context.Context,
    imageManager images.ImageManager,
    serverOrImage string,
    caCertPath string,
    imageName string,
    dryRun bool,
    config *BuildConfig,  // New parameter
) (string, error) {
    // Apply profiles and configuration
    templateData := createTemplateDataWithConfig(
        transportType,
        packageName,
        caCertPath,
        config,
    )
    // ... rest of function
}

type BuildConfig struct {
    Profiles  []string
    Variables map[string]string
    Stages    StagesConfig
}
```

### Implementation Phases

#### Phase 1: Core Infrastructure (Week 1-2)
- [ ] Implement `StageConfig` and `StagesConfig` structures
- [ ] Create template engine with variable expansion
- [ ] Update `TemplateData` structure
- [ ] Implement profile merging logic

#### Phase 2: Template Updates (Week 2-3)
- [ ] Update `npx.tmpl` with stage configuration support
- [ ] Update `uvx.tmpl` with stage configuration support
- [ ] Update `go.tmpl` with stage configuration support
- [ ] Add template helper functions

#### Phase 3: CLI Integration (Week 3-4)
- [ ] Add `--profile` flag to both `run` and `build` commands
- [ ] Add `--var` flag for variables to both commands
- [ ] Add `--config-file` flag to both commands
- [ ] Implement inline stage configuration flags
- [ ] Update `BuildFlags` struct in `cmd/thv/app/build.go`
- [ ] Update `runner.BuildFromProtocolSchemeWithName` to accept configuration

#### Phase 4: Built-in Profiles (Week 4)
- [ ] Create npm-private profile
- [ ] Create pypi-private profile
- [ ] Create go-corporate profile
- [ ] Add profile validation

#### Phase 5: Testing & Documentation (Week 5)
- [ ] Unit tests for profile merging
- [ ] Integration tests with mock registries
- [ ] E2E tests for each package manager
- [ ] Test `thv build` with profiles and configurations
- [ ] User documentation and examples for both commands

## Benefits

### For Users
1. **Simple Cases Stay Simple**: `thv run npx://package` still works
2. **Progressive Complexity**: Add configuration as needed
3. **Reusable Configurations**: Share profiles across teams
4. **No Code Changes**: New requirements handled via configuration

### For Maintainers
1. **Reduced Maintenance**: No code changes for new registry types
2. **Clear Separation**: Configuration vs implementation
3. **Testable**: Configuration can be tested independently
4. **Extensible**: Easy to add new profile types

## Security Considerations

1. **Secret Management**
   - Variables support environment variable expansion: `${ENV:VAR_NAME}`
   - Secrets only in builder stage, never in runtime
   - Support for external secret providers

2. **Profile Validation**
   - Validate commands before execution
   - Restrict certain commands in profiles
   - Audit trail for profile usage

3. **Network Security**
   - Respect existing network isolation flags
   - Support for proxy configurations
   - CA certificate handling unchanged

## Testing Strategy

### Unit Tests
```go
func TestProfileMerging(t *testing.T) {
    // Test that profiles merge correctly
    // Test variable expansion
    // Test conditional execution
}

func TestStageConfiguration(t *testing.T) {
    // Test environment variable setting
    // Test command execution order
    // Test file mounting
}
```

### Integration Tests
```go
func TestNPMPrivateRegistry(t *testing.T) {
    // Start mock NPM registry
    // Apply npm-private profile
    // Verify package installed from mock
}

func TestProfileInheritance(t *testing.T) {
    // Test profile extends functionality
    // Test override behavior
}
```

### E2E Tests
- Test with real private registries (in CI)
- Test profile combinations
- Test error scenarios

## Migration Path

1. **Phase 1**: Release with backward compatibility
   - Existing commands work unchanged
   - New profile system opt-in

2. **Phase 2**: Deprecation notices
   - Warn about future changes
   - Provide migration guide

3. **Phase 3**: Full migration
   - Remove old configuration methods
   - Profiles become primary method

## Example Use Cases

### 1. Enterprise NPM Registry

#### Building the image
```bash
thv build npx://@company/tool \
  --profile npm-private \
  --var NPM_REGISTRY_URL=https://npm.company.com \
  --tag company/tool:latest
```

#### Running directly
```bash
thv run npx://@company/tool \
  --profile npm-private \
  --var NPM_REGISTRY_URL=https://npm.company.com \
  --secret NPM_TOKEN=npm-auth-token
```

### 2. Python with Multiple Indexes

#### Configuration file approach
```yaml
# config.yaml
stages:
  builder:
    env:
      PIP_INDEX_URL: https://pypi.company.com/simple
      PIP_EXTRA_INDEX_URL: https://pypi.org/simple
      PIP_TRUSTED_HOST: pypi.company.com
```

```bash
# Build with config file
thv build uvx://internal-tool --config-file config.yaml --tag internal/tool:latest

# Run with config file
thv run uvx://internal-tool --config-file config.yaml
```

### 3. Go with Private Modules
```bash
# Build
thv build go://github.company.com/tool \
  --profile go-corporate \
  --var GO_PROXY=https://proxy.company.com \
  --var GO_PRIVATE=github.company.com \
  --tag company/go-tool:latest

# Run
thv run go://github.company.com/tool \
  --profile go-corporate \
  --var GO_PROXY=https://proxy.company.com \
  --var GO_PRIVATE=github.company.com
```

### 4. Complex Multi-Stage Configuration
```yaml
# Full configuration example
profiles:
  - base-security
  - npm-private

variables:
  NPM_REGISTRY: https://npm.company.com
  BUILD_ENV: production

stages:
  builder:
    env:
      NODE_ENV: ${BUILD_ENV}
      NPM_CONFIG_REGISTRY: ${NPM_REGISTRY}
    files:
      - source: ./npmrc
        dest: /root/.npmrc
        mode: "600"
    setup:
      - run: npm config set registry ${NPM_REGISTRY}
      - run: npm config set strict-ssl true
    pre_install:
      - run: npm audit
    post_install:
      - run: npm prune --production
      - run: rm -f /root/.npmrc  # Clean up secrets
  
  runtime:
    env:
      NODE_ENV: production
    files:
      - source: ./app-config.json
        dest: /app/config.json
```

## Conclusion

This generalized solution provides a flexible, extensible system for template customization that:
- Solves the immediate NPM registry need
- Supports all package managers equally
- Enables future customization without code changes
- Maintains security and best practices
- Preserves backward compatibility

The stage-aware configuration system respects Docker's multi-stage build patterns while providing the flexibility needed for enterprise environments.