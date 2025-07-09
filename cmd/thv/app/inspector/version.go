// Package inspector contains definitions for the inspector command.
package inspector

// Image specifies the image to use for the inspector command.
// TODO: This could probably be a flag with a sensible default
// TODO: Additionally, when the inspector image has been published
// TODO: to docker.io, we can use that instead of npx
// TODO: https://github.com/modelcontextprotocol/inspector/issues/237
// Pinning to a specific version for stability. The latest version
// as of 2025-07-09 broke the inspector command.
var Image = "npx://@modelcontextprotocol/inspector@0.15.0"
