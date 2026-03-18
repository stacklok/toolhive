// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/runner"
)

var buildCmd = &cobra.Command{
	Use:   "build [flags] PROTOCOL [-- ARGS...]",
	Short: "Build a container for an MCP server without running it",
	Long: `Build a container for an MCP server using a protocol scheme without running it.

ToolHive supports building containers from protocol schemes:

	$ thv build uvx://package-name
	$ thv build npx://package-name
	$ thv build go://package-name
	$ thv build go://./local-path

Automatically generates a container that can run the specified package
using either uvx (Python with uv package manager), npx (Node.js),
or go (Golang). For Go, you can also specify local paths starting
with './' or '../' to build local Go projects.

Build-time arguments can be baked into the container's ENTRYPOINT:

	$ thv build npx://@launchdarkly/mcp-server -- start
	$ thv build uvx://package -- --transport stdio

These arguments become part of the container image and will always run,
with runtime arguments (from 'thv run -- <args>') appending after them.

The container will be built and tagged locally, ready to be used with 'thv run'
or other container tools. The built image name will be displayed upon successful completion.

Examples:
	$ thv build uvx://mcp-server-git
	$ thv build --tag my-custom-name:latest npx://@modelcontextprotocol/server-filesystem
	$ thv build go://./my-local-server
	$ thv build npx://@launchdarkly/mcp-server -- start`,
	Args: cobra.MinimumNArgs(1),
	RunE: buildCmdFunc,
	// Ignore unknown flags to allow passing args after --
	FParseErrWhitelist: cobra.FParseErrWhitelist{
		UnknownFlags: true,
	},
}

var buildFlags BuildFlags

// BuildFlags holds the configuration for building MCP server containers
type BuildFlags struct {
	Tag    string
	Output string
	DryRun bool
}

func init() {
	// Add build flags
	AddBuildFlags(buildCmd, &buildFlags)
}

// AddBuildFlags adds all the build flags to a command
func AddBuildFlags(cmd *cobra.Command, config *BuildFlags) {
	cmd.Flags().StringVarP(&config.Tag, "tag", "t", "", "Name and optionally a tag in the 'name:tag' format for the built image "+
		"(default generates a unique image name based on the package and transport type)")
	cmd.Flags().StringVarP(&config.Output, "output", "o", "", "Write the Dockerfile to the specified file instead of building "+
		"(default builds an image instead of generating a Dockerfile)")
	cmd.Flags().BoolVar(&config.DryRun, "dry-run", false, "Generate Dockerfile without building (stdout output unless -o is set) "+
		"(default false)")
}

func buildCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	protocolScheme := args[0]

	// Validate that this is a protocol scheme
	if !runner.IsImageProtocolScheme(protocolScheme) {
		return fmt.Errorf("invalid protocol scheme: %s. Supported schemes are: uvx://, npx://, go://", protocolScheme)
	}

	// Parse build arguments using os.Args to find everything after --
	buildArgs := parseCommandArguments(os.Args)
	slog.Debug(fmt.Sprintf("Build args: %v", buildArgs)) // #nosec G706 -- buildArgs are CLI arguments we control

	// Create image manager (even for dry-run, we pass it but it won't be used)
	imageManager := images.NewImageManager(ctx)

	// If dry-run or output is specified, just generate the Dockerfile
	if buildFlags.DryRun || buildFlags.Output != "" {
		dockerfileContent, err := runner.BuildFromProtocolSchemeWithName(
			ctx, imageManager, protocolScheme, "", buildFlags.Tag, buildArgs, nil, true)
		if err != nil {
			return fmt.Errorf("failed to generate Dockerfile for %s: %w", protocolScheme, err)
		}

		// Write to output file if specified
		if buildFlags.Output != "" {
			// #nosec G703 -- buildFlags.Output is a user-provided CLI flag for output path
			if err := os.WriteFile(buildFlags.Output, []byte(dockerfileContent), 0600); err != nil {
				return fmt.Errorf("failed to write Dockerfile to %s: %w", buildFlags.Output, err)
			}
			slog.Debug(fmt.Sprintf("Dockerfile written to: %s", buildFlags.Output))
		} else {
			// Output to stdout
			fmt.Print(dockerfileContent)
		}
		return nil
	}

	slog.Debug(fmt.Sprintf("Building container for protocol scheme: %s", protocolScheme))

	// Build the image using the new protocol handler with custom name
	imageName, err := runner.BuildFromProtocolSchemeWithName(
		ctx, imageManager, protocolScheme, "", buildFlags.Tag, buildArgs, nil, false)
	if err != nil {
		return fmt.Errorf("failed to build container for %s: %w", protocolScheme, err)
	}

	// Keep this log at INFO level so users see the generated image name and tag
	slog.Info(fmt.Sprintf("Successfully built container image: %s", imageName)) // #nosec G706 -- imageName is from our build process

	return nil
}
