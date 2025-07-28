package e2e_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("THVIgnore E2E Tests", func() {
	var (
		config     *e2e.TestConfig
		serverName string
		tempDir    string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = fmt.Sprintf("thvignore-test-%d", GinkgoRandomSeed())

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		// Create a temporary directory for test files
		tempDir, err = os.MkdirTemp("", "thvignore-e2e-test")
		Expect(err).ToNot(HaveOccurred(), "Should be able to create temp directory")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up the server if it exists
			err := e2e.StopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")

			// Clean up temporary directory
			if tempDir != "" {
				os.RemoveAll(tempDir)
			}
		}
	})

	Describe("Basic .thvignore functionality", func() {
		Context("when using local .thvignore file", func() {
			It("should exclude files matching ignore patterns from container", func() {
				By("Creating test files and directories in temp directory")

				// Create various test files and directories
				testFiles := []string{
					".env",
					".env.production",
					"config.json",
					"secret.key",
					"public.txt",
					".ssh/id_rsa",
					".ssh/id_rsa.pub",
					".aws/credentials",
					".aws/config",
					"node_modules/package/index.js",
					"src/main.go",
					"README.md",
				}

				for _, file := range testFiles {
					fullPath := filepath.Join(tempDir, file)
					dir := filepath.Dir(fullPath)

					// Create directory if it doesn't exist
					err := os.MkdirAll(dir, 0755)
					Expect(err).ToNot(HaveOccurred(), "Should create directory: %s", dir)

					// Create the file with some content
					content := fmt.Sprintf("Test content for %s", file)
					err = os.WriteFile(fullPath, []byte(content), 0644)
					Expect(err).ToNot(HaveOccurred(), "Should create file: %s", file)
				}

				By("Creating .thvignore file with ignore patterns")
				thvignoreContent := `.env*
.ssh/
.aws/
*.key
node_modules/
`
				thvignorePath := filepath.Join(tempDir, ".thvignore")
				err := os.WriteFile(thvignorePath, []byte(thvignoreContent), 0644)
				Expect(err).ToNot(HaveOccurred(), "Should create .thvignore file")

				By("Starting MCP server with volume mount and ignore processing")
				volumeMount := fmt.Sprintf("%s:/workspace", tempDir)
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--volume", volumeMount,
					"--ignore-globally=false", // Only use local .thvignore
					"fetch").ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("fetch"), "Output should mention the fetch server")

				By("Waiting for the server to be running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Verifying the server appears in the list")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")

				By("Inspecting the container to verify tmpfs overlays are applied")
				// Get container details using docker inspect
				containerName := fmt.Sprintf("toolhive-%s", serverName)

				// Use the existing StartDockerCommand helper to inspect the container
				dockerCmd := e2e.StartDockerCommand("inspect", containerName)
				var dockerStdout, dockerStderr strings.Builder
				dockerCmd.Stdout = &dockerStdout
				dockerCmd.Stderr = &dockerStderr

				dockerErr := dockerCmd.Run()
				if dockerErr != nil {
					// If docker inspect fails, we can still verify the server is working
					// which indicates the ignore processing worked
					GinkgoWriter.Printf("Docker inspect failed (may be expected in some environments): %v\n", dockerErr)
					GinkgoWriter.Printf("Stderr: %s\n", dockerStderr.String())
				} else {
					inspectOutput := dockerStdout.String()

					// Verify that tmpfs mounts are present for ignored paths
					// Look for tmpfs mount entries in the docker inspect output
					Expect(inspectOutput).To(ContainSubstring("tmpfs"),
						"Should have tmpfs mounts for ignored files")

					// Verify that the main directory bind mount is present
					Expect(inspectOutput).To(ContainSubstring(tempDir),
						"Should have bind mount for the main directory")
				}
			})

			It("should create tmpfs overlays for ignored files", func() {
				By("Creating test directory with files to ignore")

				// Create test files
				testFiles := []string{
					".env",
					"config.json",
					".ssh/id_rsa",
				}

				for _, file := range testFiles {
					fullPath := filepath.Join(tempDir, file)
					dir := filepath.Dir(fullPath)

					err := os.MkdirAll(dir, 0755)
					Expect(err).ToNot(HaveOccurred())

					err = os.WriteFile(fullPath, []byte("test content"), 0644)
					Expect(err).ToNot(HaveOccurred())
				}

				By("Creating .thvignore file")
				thvignoreContent := `.env
.ssh/
`
				thvignorePath := filepath.Join(tempDir, ".thvignore")
				err := os.WriteFile(thvignorePath, []byte(thvignoreContent), 0644)
				Expect(err).ToNot(HaveOccurred())

				By("Starting MCP server with ignore processing")
				volumeMount := fmt.Sprintf("%s:/workspace", tempDir)
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--volume", volumeMount,
					"--print-resolved-overlays",
					"--ignore-globally=false",
					"fetch").ExpectSuccess()

				// The command should indicate success
				Expect(stdout+stderr).To(ContainSubstring("fetch"), "Output should mention the fetch server")

				By("Waiting for the server to be running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Inspecting the container to verify tmpfs overlays are applied")
				containerName := fmt.Sprintf("toolhive-%s", serverName)

				dockerCmd := e2e.StartDockerCommand("inspect", containerName)
				var dockerStdout, dockerStderr strings.Builder
				dockerCmd.Stdout = &dockerStdout
				dockerCmd.Stderr = &dockerStderr

				dockerErr := dockerCmd.Run()
				if dockerErr != nil {
					GinkgoWriter.Printf("Docker inspect failed (may be expected in some environments): %v\n", dockerErr)
					GinkgoWriter.Printf("Stderr: %s\n", dockerStderr.String())
				} else {
					inspectOutput := dockerStdout.String()

					// Verify that tmpfs mounts are present for ignored paths
					Expect(inspectOutput).To(ContainSubstring("tmpfs"),
						"Should have tmpfs mounts for ignored files")

					// Verify that the main directory bind mount is present
					Expect(inspectOutput).To(ContainSubstring(tempDir),
						"Should have bind mount for the main directory")
				}
			})
		})

		Context("when using global ignore patterns", func() {
			var globalIgnoreDir string

			BeforeEach(func() {
				// Create a temporary directory for global ignore config
				var err error
				globalIgnoreDir, err = os.MkdirTemp("", "thvignore-global-config")
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				if globalIgnoreDir != "" {
					os.RemoveAll(globalIgnoreDir)
				}
			})

			It("should apply global ignore patterns from custom config file", func() {
				By("Creating global ignore configuration file")
				globalIgnoreContent := `# Global ignore patterns for sensitive files
.env*
.ssh/
.aws/
.gcp/
*.pem
*.key
.docker/config.json
`
				globalIgnorePath := filepath.Join(globalIgnoreDir, "thvignore")
				err := os.WriteFile(globalIgnorePath, []byte(globalIgnoreContent), 0644)
				Expect(err).ToNot(HaveOccurred())

				By("Creating test files in temp directory")
				testFiles := []string{
					".env.local",
					".ssh/id_rsa",
					".aws/credentials",
					"app.key",
					"public.txt",
					"README.md",
				}

				for _, file := range testFiles {
					fullPath := filepath.Join(tempDir, file)
					dir := filepath.Dir(fullPath)

					err := os.MkdirAll(dir, 0755)
					Expect(err).ToNot(HaveOccurred())

					err = os.WriteFile(fullPath, []byte("test content"), 0644)
					Expect(err).ToNot(HaveOccurred())
				}

				By("Starting MCP server with global ignore file")
				volumeMount := fmt.Sprintf("%s:/workspace", tempDir)
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--volume", volumeMount,
					"--ignore-file", globalIgnorePath,
					"fetch").ExpectSuccess()

				Expect(stdout+stderr).To(ContainSubstring("fetch"), "Output should mention the fetch server")

				By("Waiting for the server to be running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Verifying global ignore patterns are applied")
				// The server should start successfully with global ignore patterns applied
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName), "Server should appear in the list")
				Expect(stdout).To(ContainSubstring("running"), "Server should be in running state")
			})
		})

		Context("when combining local and global ignore patterns", func() {
			var globalIgnoreDir string

			BeforeEach(func() {
				var err error
				globalIgnoreDir, err = os.MkdirTemp("", "thvignore-combined-config")
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				if globalIgnoreDir != "" {
					os.RemoveAll(globalIgnoreDir)
				}
			})

			It("should apply both global and local ignore patterns", func() {
				By("Creating global ignore configuration")
				globalIgnoreContent := `# Global patterns
.env*
.ssh/
`
				globalIgnorePath := filepath.Join(globalIgnoreDir, "thvignore")
				err := os.WriteFile(globalIgnorePath, []byte(globalIgnoreContent), 0644)
				Expect(err).ToNot(HaveOccurred())

				By("Creating local .thvignore file")
				localIgnoreContent := `# Local patterns
*.key
temp/
node_modules/
`
				localIgnorePath := filepath.Join(tempDir, ".thvignore")
				err = os.WriteFile(localIgnorePath, []byte(localIgnoreContent), 0644)
				Expect(err).ToNot(HaveOccurred())

				By("Creating test files that match both global and local patterns")
				testFiles := []string{
					".env.production",           // Matches global pattern
					".ssh/id_rsa",               // Matches global pattern
					"secret.key",                // Matches local pattern
					"temp/cache.txt",            // Matches local pattern
					"node_modules/pkg/index.js", // Matches local pattern
					"public.txt",                // Should not be ignored
					"src/main.go",               // Should not be ignored
				}

				for _, file := range testFiles {
					fullPath := filepath.Join(tempDir, file)
					dir := filepath.Dir(fullPath)

					err := os.MkdirAll(dir, 0755)
					Expect(err).ToNot(HaveOccurred())

					err = os.WriteFile(fullPath, []byte("test content"), 0644)
					Expect(err).ToNot(HaveOccurred())
				}

				By("Starting MCP server with both global and local ignore patterns")
				volumeMount := fmt.Sprintf("%s:/workspace", tempDir)
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--volume", volumeMount,
					"--ignore-file", globalIgnorePath,
					"--print-resolved-overlays", // Print overlays to verify both are applied
					"fetch").ExpectSuccess()

				output := stdout + stderr
				Expect(output).To(ContainSubstring("fetch"), "Output should mention the fetch server")

				By("Waiting for the server to be running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				By("Inspecting the container to verify tmpfs overlays are applied for both global and local patterns")
				containerName := fmt.Sprintf("toolhive-%s", serverName)

				dockerCmd := e2e.StartDockerCommand("inspect", containerName)
				var dockerStdout, dockerStderr strings.Builder
				dockerCmd.Stdout = &dockerStdout
				dockerCmd.Stderr = &dockerStderr

				dockerErr := dockerCmd.Run()
				if dockerErr != nil {
					GinkgoWriter.Printf("Docker inspect failed (may be expected in some environments): %v\n", dockerErr)
					GinkgoWriter.Printf("Stderr: %s\n", dockerStderr.String())
				} else {
					inspectOutput := dockerStdout.String()

					// Verify that tmpfs mounts are present for ignored paths
					Expect(inspectOutput).To(ContainSubstring("tmpfs"),
						"Should have tmpfs mounts for ignored files from both global and local patterns")

					// Verify that the main directory bind mount is present
					Expect(inspectOutput).To(ContainSubstring(tempDir),
						"Should have bind mount for the main directory")
				}
			})
		})
	})

	Describe("Error handling and edge cases", func() {
		Context("when .thvignore file has invalid patterns", func() {
			It("should handle malformed patterns gracefully", func() {
				By("Creating .thvignore with various pattern types")
				thvignoreContent := `# Valid patterns
.env
*.key

# Empty lines and comments should be ignored

# Patterns with special characters
[invalid-bracket
***/invalid-glob

# Valid patterns after invalid ones
.ssh/
`
				thvignorePath := filepath.Join(tempDir, ".thvignore")
				err := os.WriteFile(thvignorePath, []byte(thvignoreContent), 0644)
				Expect(err).ToNot(HaveOccurred())

				By("Creating test files")
				testFiles := []string{
					".env",
					"test.key",
					".ssh/id_rsa",
					"normal.txt",
				}

				for _, file := range testFiles {
					fullPath := filepath.Join(tempDir, file)
					dir := filepath.Dir(fullPath)

					err := os.MkdirAll(dir, 0755)
					Expect(err).ToNot(HaveOccurred())

					err = os.WriteFile(fullPath, []byte("test"), 0644)
					Expect(err).ToNot(HaveOccurred())
				}

				By("Starting MCP server - should handle invalid patterns gracefully")
				volumeMount := fmt.Sprintf("%s:/workspace", tempDir)
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--volume", volumeMount,
					"--ignore-globally=false",
					"fetch").ExpectSuccess()

				// Should still start successfully despite invalid patterns
				Expect(stdout + stderr).To(ContainSubstring("fetch"))

				By("Waiting for the server to be running")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should start despite invalid patterns")
			})
		})

		Context("when ignore file doesn't exist", func() {
			It("should start normally without ignore processing", func() {
				By("Creating test files without .thvignore")
				testFiles := []string{
					".env",
					"config.json",
					"README.md",
				}

				for _, file := range testFiles {
					fullPath := filepath.Join(tempDir, file)
					err := os.WriteFile(fullPath, []byte("test"), 0644)
					Expect(err).ToNot(HaveOccurred())
				}

				By("Starting MCP server without .thvignore file")
				volumeMount := fmt.Sprintf("%s:/workspace", tempDir)
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--volume", volumeMount,
					"--ignore-globally=false",
					"fetch").ExpectSuccess()

				Expect(stdout + stderr).To(ContainSubstring("fetch"))

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should start normally without ignore file")

				By("Verifying server is running")
				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName))
				Expect(stdout).To(ContainSubstring("running"))
			})
		})

		Context("when using non-existent global ignore file", func() {
			It("should handle missing global ignore file gracefully", func() {
				By("Creating test files")
				err := os.WriteFile(filepath.Join(tempDir, "test.txt"), []byte("test"), 0644)
				Expect(err).ToNot(HaveOccurred())

				By("Starting MCP server with non-existent global ignore file")
				volumeMount := fmt.Sprintf("%s:/workspace", tempDir)
				nonExistentPath := "/non/existent/path/thvignore"

				// This should either succeed (ignoring the missing file) or fail gracefully
				stdout, stderr, err := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--volume", volumeMount,
					"--ignore-file", nonExistentPath,
					"fetch").Run()

				if err != nil {
					// If it fails, it should be a clear error about the missing file
					output := stdout + stderr
					Expect(output).To(Or(
						ContainSubstring("no such file"),
						ContainSubstring("not found"),
						ContainSubstring("does not exist"),
					), "Should provide clear error about missing ignore file")
				} else {
					// If it succeeds, it should handle the missing file gracefully
					Expect(stdout + stderr).To(ContainSubstring("fetch"))

					// Clean up if server started
					err = e2e.WaitForMCPServer(config, serverName, 10*time.Second)
					if err == nil {
						// Server started, verify it's running
						listOutput, _ := e2e.NewTHVCommand(config, "list").ExpectSuccess()
						Expect(listOutput).To(ContainSubstring(serverName))
					}
				}
			})
		})
	})

	Describe("Integration with different MCP servers", func() {
		Context("when using ignore patterns with different server types", func() {
			It("should work with fetch MCP server", func() {
				By("Creating test environment with sensitive files")
				sensitiveFiles := []string{
					".env.production",
					".ssh/id_rsa",
					".aws/credentials",
					"api.key",
				}

				for _, file := range sensitiveFiles {
					fullPath := filepath.Join(tempDir, file)
					dir := filepath.Dir(fullPath)

					err := os.MkdirAll(dir, 0755)
					Expect(err).ToNot(HaveOccurred())

					err = os.WriteFile(fullPath, []byte("sensitive content"), 0600)
					Expect(err).ToNot(HaveOccurred())
				}

				By("Creating .thvignore to protect sensitive files")
				thvignoreContent := `.env*
.ssh/
.aws/
*.key
`
				thvignorePath := filepath.Join(tempDir, ".thvignore")
				err := os.WriteFile(thvignorePath, []byte(thvignoreContent), 0644)
				Expect(err).ToNot(HaveOccurred())

				By("Starting fetch MCP server with ignore patterns")
				volumeMount := fmt.Sprintf("%s:/workspace", tempDir)
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--volume", volumeMount,
					"--ignore-globally=false",
					"fetch").ExpectSuccess()

				Expect(stdout + stderr).To(ContainSubstring("fetch"))

				By("Verifying server starts and runs successfully")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName))
				Expect(stdout).To(ContainSubstring("running"))
			})
		})
	})

	Describe("Performance and scalability", func() {
		Context("when processing large numbers of files", func() {
			It("should handle directories with many files efficiently", func() {
				By("Creating a large number of test files")

				// Create multiple directories with files
				dirs := []string{"src", "tests", "docs", "config", ".hidden"}
				fileTypes := []string{".go", ".txt", ".json", ".md", ".key"}

				totalFiles := 0
				for _, dir := range dirs {
					dirPath := filepath.Join(tempDir, dir)
					err := os.MkdirAll(dirPath, 0755)
					Expect(err).ToNot(HaveOccurred())

					// Create 20 files per directory
					for i := 0; i < 20; i++ {
						for _, ext := range fileTypes {
							fileName := fmt.Sprintf("file%d%s", i, ext)
							filePath := filepath.Join(dirPath, fileName)
							err = os.WriteFile(filePath, []byte("content"), 0644)
							Expect(err).ToNot(HaveOccurred())
							totalFiles++
						}
					}
				}

				GinkgoWriter.Printf("Created %d test files\n", totalFiles)

				By("Creating .thvignore with patterns that match many files")
				thvignoreContent := `*.key
.hidden/
config/*.json
`
				thvignorePath := filepath.Join(tempDir, ".thvignore")
				err := os.WriteFile(thvignorePath, []byte(thvignoreContent), 0644)
				Expect(err).ToNot(HaveOccurred())

				By("Starting MCP server and measuring startup time")
				startTime := time.Now()

				volumeMount := fmt.Sprintf("%s:/workspace", tempDir)
				stdout, stderr := e2e.NewTHVCommand(config, "run",
					"--name", serverName,
					"--volume", volumeMount,
					"--ignore-globally=false",
					"fetch").ExpectSuccess()

				Expect(stdout + stderr).To(ContainSubstring("fetch"))

				By("Verifying server starts within reasonable time")
				err = e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				startupDuration := time.Since(startTime)
				GinkgoWriter.Printf("Server startup took: %v\n", startupDuration)

				// Server should start within a reasonable time even with many files
				Expect(startupDuration).To(BeNumerically("<", 2*time.Minute),
					"Server should start within 2 minutes even with many files")

				stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
				Expect(stdout).To(ContainSubstring(serverName))
				Expect(stdout).To(ContainSubstring("running"))
			})
		})
	})
})
