# Registry JSON Schema

This document describes the [JSON Schema](https://json-schema.org/) for the
ToolHive MCP server registry and how to use it for validation and development.

## Overview

The [`schema.json`](schema.json) file provides comprehensive validation for the
[`registry.json`](../../pkg/registry/data/registry.json) file structure. It
ensures consistency, catches common errors, and serves as living documentation
for contributors.

This can also be used to validate a custom registry file to be used with the
[`thv config set-registry-url`](../cli/thv_config_set-registry-url.md) command.

## Schema location

- **File**: [`docs/registry/schema.json`](schema.json)
- **Schema ID**:
  `https://raw.githubusercontent.com/stacklok/toolhive/main/docs/registry/schema.json`

## Usage

### Local validation

#### Using check-jsonschema

Install check-jsonschema via Homebrew (macOS):

```bash
brew install check-jsonschema
```

Or via pipx (cross-platform):

```bash
pipx install check-jsonschema
```

Validate the registry with full format validation:

```bash
# Run from the root of the repository
check-jsonschema --schemafile docs/registry/schema.json pkg/registry/data/registry.json
```

#### Using ajv-cli

Install ajv-cli and ajv-formats globally:

```bash
npm install -g ajv-cli ajv-formats
```

Validate the registry with format validation:

```bash
# Run from the root of the repository
ajv validate -c ajv-formats -s docs/registry/schema.json -d pkg/registry/data/registry.json
```

#### Using VS Code

VS Code automatically validates JSON files when a schema is specified. Add this
to the top of any registry JSON file:

```json
{
  "$schema": "https://raw.githubusercontent.com/stacklok/toolhive/main/docs/registry/schema.json",
  ...
}
```

## Methodology

The `draft-07` version of JSON Schema is used to ensure the widest compatibility
with commonly used tools and libraries.

The schema is currently maintained manually, due to differences in how required
vs. optional sections are defined in the Go codebase (`omitempty` vs. nil/empty
conditional checks).

At some point, we may automate this process by generating the schema from the Go
code using something like
[invopop/jsonschema](https://github.com/invopop/jsonschema), but for now, manual
updates are necessary to ensure accuracy and completeness.

## Contributing

When adding new server entries:

1. **Validate locally** before submitting PRs
2. **Follow naming conventions** for consistency
3. **Include comprehensive descriptions** for clarity
4. **Specify minimal permissions** for security
5. **Use appropriate tags** for discoverability

## Related documentation

- [Registry Management Process](management.md)
- [Registry Inclusion Heuristics](heuristics.md)
- [JSON Schema Specification](https://json-schema.org/)
