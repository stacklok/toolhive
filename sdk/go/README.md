# ToolHive Go SDK

`github.com/stacklok/toolhive/sdk/go` is the nested module containing ToolHive's generated Go client. Its public request, response, and operation types are generated from the complete canonical OpenAPI 3.1 contract in `openapi.json`; do not add hand-written endpoint wrappers or duplicate DTOs.

## Install

```bash
go get github.com/stacklok/toolhive/sdk/go@<commit-or-pseudo-version>
```

Import the generated client package as `github.com/stacklok/toolhive/sdk/go/client`. No SDK tag has been published: pre-release users must pin a ToolHive commit or Go pseudo-version compatible with their ToolHive version. SDK release/tag automation is intentionally deferred.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/stacklok/toolhive/sdk/go/client"
)

func main() {
	api, err := client.NewClient("http://127.0.0.1:8080")
	if err != nil {
		log.Fatal(err)
	}

	health, err := api.GetHealth(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(health)
}
```

## Authentication and transport

ToolHive deployments choose their own authentication mechanism. Configure headers, mTLS, proxies, and credentials on a caller-owned `*http.Client` and pass it with the generated `client.WithClient` option. The SDK never logs credentials or request bodies.

`client.NewClient` validates the absolute HTTP(S) base URL, gives requests without an earlier context deadline a 30-second deadline, and applies a 10 MiB response-body limit by default. Use `client.WithMaxResponseBodyBytes` to set a different positive limit while retaining URL validation and default timeout behavior. A response that exceeds the configured limit returns `client.ErrResponseBodyTooLarge` rather than silently truncating data; this also applies to workload-log endpoints. Caller-supplied transports and authentication remain composed through `client.WithClient`. `client.NewUnsafeClient` intentionally bypasses these safeguards and is only appropriate when the caller implements an equivalent policy, such as deliberately streaming a response.

`AddRegistry` remains present for API compatibility. The server accepts no request body and always returns `501 Not Implemented` because custom registries are not currently supported.

## Generate and verify

From the repository root:

```bash
task docs        # refreshes the OpenAPI docs and SDK snapshot/client
task sdk-test
task sdk-lint
task sdk-verify
```

The generation executable is pinned to `github.com/ogen-go/ogen/cmd/ogen@v1.15.0`, which supports OpenAPI 3.1. The normalization command gives every operation a stable operation ID, applies deliberate domain-facing schema names, preserves references, and fixes Swag's invalid request-body `oneOf` form before generation. A source-controlled generation step renames Ogen's raw constructor to `NewUnsafeClient` so `NewClient` can apply the SDK safety policy without editing generated output.

See [`examples/register-client`](examples/register-client) for a compiling caller-supplied-client example that handles a non-2xx response.
