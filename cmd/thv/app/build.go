package app

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/runner"
)

var buildCmd = &cobra.Command{
	Use:   "build [flags] PROTOCOL",
	Short: "Build a container for an MCP server without running it",
	Long: `Build a container for an MCP server using a protocol scheme without running it.

ToolHive supports building containers from protocol schemes:

	$ thv build uvx://package-name
	$ thv build npx://package-name
	$ thv build go://package-name
	$ thv build go://./local-path
	$ thv build maven://com.example.MCPServer
	$ thv build gradle://com.example.MCPServer

Automatically generates a container that can run the specified package
using either uvx (Python with uv package manager), npx (Node.js),
go (Golang), maven (Java with Maven), or gradle (Java with Gradle).
For Go, you can also specify local paths starting with './' or '../'
to build local Go projects.

The container will be built and tagged locally, ready to be used with 'thv run'
or other container tools. The built image name will be displayed upon successful completion.

Examples:
	$ thv build uvx://mcp-server-git
	$ thv build --tag my-custom-name:latest npx://@modelcontextprotocol/server-filesystem
	$ thv build go://./my-local-server
	$ thv build maven://com.example.MCPServer
	$ thv build gradle://com.example.MCPServer`,
	Args: cobra.ExactArgs(1),
	RunE: buildCmdFunc,
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
	cmd.Flags().StringVarP(&config.Tag, "tag", "t", "", "Name and optionally a tag in the 'name:tag' format for the built image")
	cmd.Flags().StringVarP(&config.Output, "output", "o", "", "Write the Dockerfile to the specified file instead of building")
	cmd.Flags().BoolVar(&config.DryRun, "dry-run", false, "Generate Dockerfile without building (stdout output unless -o is set)")
}

func buildCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	protocolScheme := args[0]

	// Validate that this is a protocol scheme
	if !runner.IsImageProtocolScheme(protocolScheme) {
		return fmt.Errorf("invalid protocol scheme: %s. Supported schemes are: %s", protocolScheme, runner.GetSupportedSchemes())
	}

	// Create image manager (even for dry-run, we pass it but it won't be used)
	imageManager := images.NewImageManager(ctx)

	// If dry-run or output is specified, just generate the Dockerfile
	if buildFlags.DryRun || buildFlags.Output != "" {
		dockerfileContent, err := runner.BuildFromProtocolSchemeWithName(ctx, imageManager, protocolScheme, "", buildFlags.Tag, true)
		if err != nil {
			return fmt.Errorf("failed to generate Dockerfile for %s: %v", protocolScheme, err)
		}

		// Write to output file if specified
		if buildFlags.Output != "" {
			if err := os.WriteFile(buildFlags.Output, []byte(dockerfileContent), 0600); err != nil {
				return fmt.Errorf("failed to write Dockerfile to %s: %v", buildFlags.Output, err)
			}
			logger.Infof("Dockerfile written to: %s", buildFlags.Output)
			fmt.Printf("Dockerfile written to: %s\n", buildFlags.Output)
		} else {
			// Output to stdout
			fmt.Print(dockerfileContent)
		}
		return nil
	}

	logger.Infof("Building container for protocol scheme: %s", protocolScheme)

	// Build the image using the new protocol handler with custom name
	imageName, err := runner.BuildFromProtocolSchemeWithName(ctx, imageManager, protocolScheme, "", buildFlags.Tag, false)
	if err != nil {
		return fmt.Errorf("failed to build container for %s: %v", protocolScheme, err)
	}

	logger.Infof("Successfully built container image: %s", imageName)
	fmt.Printf("Container built successfully: %s\n", imageName)
	fmt.Printf("You can now run it with: thv run %s\n", imageName)

	return nil
}
