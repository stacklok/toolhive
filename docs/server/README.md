# ToolHive Server API Documentation

ToolHive uses OpenAPI 3.1.0 for API documentation. The documentation is generated using [swag](https://github.com/swaggo/swag) and served using [Scalar](https://github.com/scalar/scalar).

## Prerequisites

Install the required tools:

```bash
# Install swag for OpenAPI generation
go install github.com/swaggo/swag/v2/cmd/swag@v2.0.0-rc4
```

## Generating Documentation

1. Add OpenAPI annotations to your code following the [swag documentation](https://github.com/swaggo/swag#declarative-comments-format)

2. Generate the OpenAPI specification:

   ```bash
   # at the root of the repository run:
   swag init -g pkg/api/server.go --v3.1 -o docs/server
   ```

   This will generate:

   - `docs/swagger.json`: OpenAPI 3.1.0 specification
   - `docs/swagger.yaml`: YAML version of the specification
   - `docs/docs.go`: Go code containing the specification

## Viewing Documentation

1. Start the server with OpenAPI docs enabled:

   ```bash
   thv serve --openapi
   ```

2. Access the documentation:
   - OpenAPI JSON spec: `http://localhost:8080/api/openapi.json`
   - Scalar UI: `http://localhost:8080/api/doc`

## Best Practices

1. Always document:

   - Request/response schemas
   - Error responses
   - Authentication requirements
   - Query parameters
   - Path parameters

2. Use descriptive summaries and descriptions

3. Group related endpoints using tags

4. Keep the documentation up to date with code changes

## Troubleshooting

If the documentation is not updating:

1. Check that your annotations are correct
2. Verify that you're using the correct version of swag
3. Make sure you're running `swag init` from the correct directory
4. Check that the generated files are being included in your build
