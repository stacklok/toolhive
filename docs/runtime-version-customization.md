# Runtime Version Customization

This guide explains how to customize the base images and packages used when running MCP servers with protocol schemes (`uvx://`, `npx://`, `go://`).

## Overview

When you use protocol schemes like `thv run go://github.com/example/server`, ToolHive automatically generates a container image. By default, it uses:

- **Go**: `golang:1.25-alpine` (builder), `alpine:3.22` (runtime)
- **Node**: `node:22-alpine` (builder and runtime)
- **Python**: `python:3.13-slim` (builder and runtime)

You can customize these base images to use different versions or add additional build packages.

## Use Cases

- **Version compatibility**: Use older runtime versions for compatibility with legacy code
- **Newer features**: Use latest runtime versions to access new language features
- **Build dependencies**: Add compiler tools, native libraries, or build utilities
- **Corporate requirements**: Use internally mirrored or hardened base images

## CLI Flags

### `--runtime-image`

Override the default base image for the builder stage.

**Examples:**

```bash
# Use Go 1.23 instead of default 1.25
thv run go://github.com/example/server --runtime-image golang:1.23-alpine

# Use Node 20 LTS instead of default 22
thv run npx://@modelcontextprotocol/server-memory --runtime-image node:20-alpine

# Use Python 3.11 for compatibility
thv run uvx://mcp-server-sqlite --runtime-image python:3.11-slim
```

### `--runtime-add-package`

Add additional packages to install during the build stage. Can be repeated multiple times.

**Examples:**

```bash
# Add build tools for native extensions
thv run go://github.com/example/server \
  --runtime-image golang:1.24-alpine \
  --runtime-add-package gcc \
  --runtime-add-package musl-dev

# Add multiple packages for Python C extensions
thv run uvx://numpy-based-server \
  --runtime-image python:3.12-slim \
  --runtime-add-package build-essential \
  --runtime-add-package libopenblas-dev
```

## Configuration File

You can set default runtime configurations in `~/.toolhive/config.yaml`:

```yaml
runtime_configs:
  go:
    builder_image: "golang:1.24-alpine"
    additional_packages:
      - ca-certificates
      - git
      - gcc

  node:
    builder_image: "node:20-alpine"
    additional_packages:
      - git
      - python3
      - make

  python:
    builder_image: "python:3.11-slim"
    additional_packages:
      - ca-certificates
      - git
      - gcc
```

When set, these become your new defaults for all protocol scheme workloads.

## Configuration Priority

Runtime configurations are resolved in this order (highest priority first):

1. **CLI flags** (`--runtime-image`, `--runtime-add-package`)
2. **User config file** (`~/.toolhive/config.yaml`)
3. **Built-in defaults** (latest stable versions)

## Important Notes

### Go Runtime Image

For Go workloads, **only the builder image is customizable**. The runtime stage always uses `alpine:3.22` because:

- Go produces static binaries that don't require the Go toolchain at runtime
- A minimal Alpine runtime keeps images small and secure
- This simplicity reduces attack surface and maintenance burden

If you need a different runtime environment, use a custom container image instead of the `go://` protocol scheme.

### Package Manager Detection

ToolHive automatically detects the package manager based on the base image:

- **Alpine-based** images (containing `alpine` in name): Uses `apk`
- **Debian/Ubuntu-based** images (containing `slim`, `debian`, or `ubuntu`): Uses `apt-get`
- **Default**: Assumes Debian/Ubuntu and uses `apt-get`

Package names must match the detected package manager. For example:
- Alpine: `gcc`, `musl-dev`, `git`
- Debian: `build-essential`, `libssl-dev`, `git`

## Examples

### Legacy Python Application

```bash
# Run old Python app requiring Python 3.9
thv run uvx://legacy-mcp-server --runtime-image python:3.9-slim
```

### Go App with CGO Dependencies

```bash
# Build Go app that needs CGO and SQLite
thv run go://github.com/example/sqlite-server \
  --runtime-image golang:1.25-alpine \
  --runtime-add-package gcc \
  --runtime-add-package musl-dev \
  --runtime-add-package sqlite-dev
```

### Node App with Native Modules

```bash
# Build Node app with native addons
thv run npx://native-addon-server \
  --runtime-image node:22-alpine \
  --runtime-add-package python3 \
  --runtime-add-package make \
  --runtime-add-package g++
```

### Corporate Custom Images

```bash
# Use internal mirror with security patches
thv run go://github.com/example/server \
  --runtime-image registry.company.com/golang:1.25-alpine-hardened
```

## Troubleshooting

### Package Not Found

**Error**: `apk: command not found` or `apt-get: command not found`

**Cause**: Wrong package manager for the base image

**Solution**: Use the correct package names for your base image's package manager, or use a different base image

### Build Failures

**Error**: `cannot find package` or compilation errors

**Cause**: Missing build dependencies

**Solution**: Add required packages with `--runtime-add-package`

### Version Incompatibilities

**Error**: Application fails at runtime with version-related errors

**Cause**: Runtime version too old or too new

**Solution**: Try different runtime versions until you find one that works

## Related Commands

- `thv run --help` - See all run command options
- `thv export <workload>` - Export workload config including runtime settings
- `thv list` - List all running workloads

## See Also

- [RunConfig Documentation](arch/05-runconfig-and-permissions.md) - Complete RunConfig reference
- [Protocol Schemes](../README.md#protocol-schemes) - Overview of uvx://, npx://, and go:// schemes
