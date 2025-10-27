package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Export Command", Label("core", "export", "e2e"), func() {
	var (
		config     *e2e.TestConfig
		serverName string
		tempDir    string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = generateExportTestServerName("export-test")
		tempDir = GinkgoT().TempDir()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up the server if it exists
			err := e2e.StopAndRemoveMCPServer(config, serverName)
			Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove server")
		}
	})

	Describe("Exporting server configurations", func() {
		Context("when exporting as JSON (default format)", func() {
			It("should export a valid RunConfig JSON", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Exporting the server configuration to JSON")
				exportPath := filepath.Join(tempDir, "export.json")
				stdout, _ = e2e.NewTHVCommand(config, "export", serverName, exportPath).ExpectSuccess()
				Expect(stdout).To(ContainSubstring("Successfully exported run configuration"))

				By("Verifying the exported file exists and is valid JSON")
				Expect(exportPath).To(BeAnExistingFile())

				fileContent, err := os.ReadFile(exportPath)
				Expect(err).ToNot(HaveOccurred())

				var runConfig runner.RunConfig
				err = json.Unmarshal(fileContent, &runConfig)
				Expect(err).ToNot(HaveOccurred(), "Exported file should be valid JSON")

				By("Verifying the exported configuration contains expected fields")
				Expect(runConfig.Image).ToNot(BeEmpty(), "Image should be set")
				Expect(runConfig.Name).To(Equal(serverName), "Name should match")
				Expect(runConfig.Transport).ToNot(BeEmpty(), "Transport should be set")
				Expect(runConfig.SchemaVersion).ToNot(BeEmpty(), "Schema version should be set")
			})
		})

		Context("when exporting as Kubernetes manifest", func() {
			It("should export a valid MCPServer YAML", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Exporting the server configuration to Kubernetes YAML")
				exportPath := filepath.Join(tempDir, "export.yaml")
				stdout, _ = e2e.NewTHVCommand(config, "export", serverName, exportPath, "--format", "k8s").ExpectSuccess()
				Expect(stdout).To(ContainSubstring("Successfully exported Kubernetes MCPServer resource"))

				By("Verifying the exported file exists and is valid YAML")
				Expect(exportPath).To(BeAnExistingFile())

				fileContent, err := os.ReadFile(exportPath)
				Expect(err).ToNot(HaveOccurred())

				var mcpServer v1alpha1.MCPServer
				err = yaml.Unmarshal(fileContent, &mcpServer)
				Expect(err).ToNot(HaveOccurred(), "Exported file should be valid YAML")

				By("Verifying the exported MCPServer has correct structure")
				Expect(mcpServer.APIVersion).To(Equal("toolhive.stacklok.dev/v1alpha1"))
				Expect(mcpServer.Kind).To(Equal("MCPServer"))
				Expect(mcpServer.Name).ToNot(BeEmpty(), "Name should be set")
				Expect(mcpServer.Spec.Image).ToNot(BeEmpty(), "Image should be set")
				Expect(mcpServer.Spec.Transport).ToNot(BeEmpty(), "Transport should be set")
			})
		})

		Context("when exporting a server with environment variables", func() {
			It("should include environment variables in the export", func() {
				By("Starting a server with environment variables")
				stdout, stderr := e2e.NewTHVCommand(config,
					"run",
					"--name", serverName,
					"--env", "TEST_VAR=test_value",
					"--env", "ANOTHER_VAR=another_value",
					"osv",
				).ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Exporting as JSON and verifying environment variables")
				jsonPath := filepath.Join(tempDir, "with-env.json")
				e2e.NewTHVCommand(config, "export", serverName, jsonPath).ExpectSuccess()

				fileContent, err := os.ReadFile(jsonPath)
				Expect(err).ToNot(HaveOccurred())

				var runConfig runner.RunConfig
				err = json.Unmarshal(fileContent, &runConfig)
				Expect(err).ToNot(HaveOccurred())

				Expect(runConfig.EnvVars).To(HaveKey("TEST_VAR"))
				Expect(runConfig.EnvVars["TEST_VAR"]).To(Equal("test_value"))
				Expect(runConfig.EnvVars).To(HaveKey("ANOTHER_VAR"))
				Expect(runConfig.EnvVars["ANOTHER_VAR"]).To(Equal("another_value"))

				By("Exporting as Kubernetes and verifying environment variables")
				yamlPath := filepath.Join(tempDir, "with-env.yaml")
				e2e.NewTHVCommand(config, "export", serverName, yamlPath, "--format", "k8s").ExpectSuccess()

				fileContent, err = os.ReadFile(yamlPath)
				Expect(err).ToNot(HaveOccurred())

				var mcpServer v1alpha1.MCPServer
				err = yaml.Unmarshal(fileContent, &mcpServer)
				Expect(err).ToNot(HaveOccurred())

				Expect(mcpServer.Spec.Env).ToNot(BeEmpty())
				envMap := make(map[string]string)
				for _, env := range mcpServer.Spec.Env {
					envMap[env.Name] = env.Value
				}
				Expect(envMap).To(HaveKey("TEST_VAR"))
				Expect(envMap["TEST_VAR"]).To(Equal("test_value"))
				Expect(envMap).To(HaveKey("ANOTHER_VAR"))
				Expect(envMap["ANOTHER_VAR"]).To(Equal("another_value"))
			})
		})

		Context("when exporting with invalid format", func() {
			It("should fail with an appropriate error", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Attempting to export with an invalid format")
				exportPath := filepath.Join(tempDir, "invalid.txt")
				_, stderr, err = e2e.NewTHVCommand(config, "export", serverName, exportPath, "--format", "invalid").ExpectFailure()
				Expect(stderr).To(ContainSubstring("invalid format"))
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when exporting a non-existent server", func() {
			It("should fail with an appropriate error", func() {
				By("Attempting to export a non-existent server")
				exportPath := filepath.Join(tempDir, "nonexistent.json")
				_, stderr, err := e2e.NewTHVCommand(config, "export", "nonexistent-server", exportPath).ExpectFailure()
				Expect(stderr).To(Or(
					ContainSubstring("not found"),
					ContainSubstring("failed to load"),
				))
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when creating nested directories for export", func() {
			It("should create the directory structure", func() {
				By("Starting an OSV MCP server")
				stdout, stderr := e2e.NewTHVCommand(config, "run", "--name", serverName, "osv").ExpectSuccess()
				Expect(stdout+stderr).To(ContainSubstring("osv"), "Output should mention the osv server")

				By("Waiting for the server to be running")
				err := e2e.WaitForMCPServer(config, serverName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred(), "Server should be running within 60 seconds")

				By("Exporting to a nested directory path")
				exportPath := filepath.Join(tempDir, "nested", "dirs", "export.json")
				stdout, _ = e2e.NewTHVCommand(config, "export", serverName, exportPath).ExpectSuccess()
				Expect(stdout).To(ContainSubstring("Successfully exported run configuration"))

				By("Verifying the nested directories were created")
				Expect(exportPath).To(BeAnExistingFile())
			})
		})
	})
})

// generateExportTestServerName creates a unique server name for export tests
func generateExportTestServerName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, GinkgoRandomSeed())
}
