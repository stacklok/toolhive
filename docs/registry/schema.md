# Registry JSON Schema

This document describes the [JSON Schema](https://json-schema.org/) for the
ToolHive MCP server registry and how to use it for validation and development.

> **⚠️ Registry Migration Notice**
>
> The ToolHive registry has been migrated to a separate repository for better management and maintenance.
>
> **To contribute MCP servers, please visit: https://github.com/stacklok/toolhive-registry**
>
> The registry data in this repository is now automatically synchronized from the external registry.

## Overview

The [`schema.json`](../../pkg/registry/data/schema.json) file provides comprehensive validation for the
[`registry.json`](../../pkg/registry/data/registry.json) file structure. It
ensures consistency, catches common errors, and serves as living documentation
for contributors.

This can also be used to validate a custom registry file to be used with the
[`thv config set-registry-url`](../cli/thv_config_set-registry-url.md) command.

## Schema location

- **File**: [`pkg/registry/data/schema.json`](../../pkg/registry/data/schema.json)
- **Schema ID**:
  `https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/schema.json`

## Usage

### Automated validation (Go tests)

The registry is automatically validated against the schema during development
and CI/CD through Go tests. This ensures that any changes to the registry data
are immediately validated.

The validation is implemented in
[`pkg/registry/schema_validation.go`](../../pkg/registry/schema_validation.go)
and tested in
[`pkg/registry/schema_validation_test.go`](../../pkg/registry/schema_validation_test.go).

**Key tests:**

- `TestEmbeddedRegistrySchemaValidation` - Validates the embedded
  `registry.json` against the schema
- `TestRegistrySchemaValidation` - Comprehensive test suite with valid and
  invalid registry examples

**Running the validation:**

```bash
# Run all schema validation tests
go test -v ./pkg/registry -run ".*Schema.*"

# Run just the embedded registry validation
go test -v ./pkg/registry -run TestEmbeddedRegistrySchemaValidation

# Run all registry tests (includes schema validation)
go test -v ./pkg/registry
```

This validation runs automatically as part of:

- Local development (`go test`)
- CI/CD pipeline (GitHub Actions)
- Pre-commit hooks (if configured)

### Manual validation

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
check-jsonschema --schemafile pkg/registry/data/schema.json pkg/registry/data/registry.json
```

#### Using ajv-cli

Install ajv-cli and ajv-formats globally:

```bash
npm install -g ajv-cli ajv-formats
```

Validate the registry with format validation:

```bash
# Run from the root of the repository
ajv validate -c ajv-formats -s pkg/registry/data/schema.json -d pkg/registry/data/registry.json
```

#### Using VS Code

VS Code automatically validates JSON files when a schema is specified. Add this
to the top of any registry JSON file:

```json
{
  "$schema": "https://raw.githubusercontent.com/stacklok/toolhive/main/pkg/registry/data/schema.json",
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

**For adding new MCP servers:**

Please visit the [toolhive-registry repository](https://github.com/stacklok/toolhive-registry) which now manages all MCP server definitions.

**For schema improvements:**

When modifying the registry schema in this repository:

1. **Validate locally** before submitting PRs
2. **Follow naming conventions** for consistency
3. **Include comprehensive descriptions** for clarity
4. **Test with existing registry data** to ensure compatibility
5. **Update documentation** to reflect schema changes

**Legacy server addition process (deprecated):**

~~When adding new server entries:~~
1. ~~**Validate locally** before submitting PRs~~
2. ~~**Follow naming conventions** for consistency~~
3. ~~**Include comprehensive descriptions** for clarity~~
4. ~~**Specify minimal permissions** for security~~
5. ~~**Use appropriate tags** for discoverability~~

## Related documentation

- [Registry Management Process](management.md)
- [Registry Inclusion Heuristics](heuristics.md)
- [JSON Schema Specification](https://json-schema.org/)
