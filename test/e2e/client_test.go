package e2e_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Client Management", func() {
	var (
		testConfig        *e2e.TestConfig
		tempXdgConfigHome string
		tempHome          string
		tempConfigDir     string
		tempConfigPath    string
	)

	BeforeEach(func() {
		testConfig = e2e.NewTestConfig()

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(testConfig)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		// Create temporary directories for config and home
		tempXdgConfigHome = GinkgoT().TempDir()
		tempHome = GinkgoT().TempDir()

		// Setup temporary config directory and file (recreating SetupTestConfig functionality)
		tempConfigDir = filepath.Join(tempXdgConfigHome, "toolhive")
		err = os.MkdirAll(tempConfigDir, 0755)
		Expect(err).ToNot(HaveOccurred())

		tempConfigPath = filepath.Join(tempConfigDir, "config.yaml")
		// Create empty config file - CLI will populate it
		err = os.WriteFile(tempConfigPath, []byte("{}"), 0600)
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("client register command", func() {
		It("should fail to register an invalid client", func() {
			// Try to register an invalid client
			_, stderr, err := e2e.NewTHVCommand(testConfig, "client", "register", "not-a-client").
				WithEnv(fmt.Sprintf("XDG_CONFIG_HOME=%s", tempXdgConfigHome)).
				WithEnv(fmt.Sprintf("HOME=%s", tempHome)).
				ExpectFailure()

			// Check that either we get invalid client type error or container runtime error
			Expect(stderr).To(Or(
				ContainSubstring("invalid client type"),
				ContainSubstring("container runtime not found"),
			))
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("client remove command", func() {
		It("should fail to remove an invalid client", func() {
			// Try to remove an invalid client
			_, stderr, err := e2e.NewTHVCommand(testConfig, "client", "remove", "not-a-client").
				WithEnv(fmt.Sprintf("XDG_CONFIG_HOME=%s", tempXdgConfigHome)).
				WithEnv(fmt.Sprintf("HOME=%s", tempHome)).
				ExpectFailure()

			// Check that either we get invalid client type error or container runtime error
			Expect(stderr).To(Or(
				ContainSubstring("invalid client type"),
				ContainSubstring("container runtime not found"),
			))
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("client list-registered command", func() {
		BeforeEach(func() {
			// Pre-populate temporary config with multiple registered clients in non-alphabetical order
			testClients := []string{"vscode", "cursor", "roo-code", "cline", "claude-code"}
			err := config.UpdateConfigAtPath(tempConfigPath, func(c *config.Config) {
				c.Clients.RegisteredClients = testClients
			})
			Expect(err).ToNot(HaveOccurred())
		})

		It("should list registered clients in alphabetical order", func() {
			// List registered clients
			stdout, _ := e2e.NewTHVCommand(testConfig, "client", "list-registered").
				WithEnv(fmt.Sprintf("XDG_CONFIG_HOME=%s", tempXdgConfigHome)).
				WithEnv(fmt.Sprintf("HOME=%s", tempHome)).
				ExpectSuccess()

			// Extract client names from table output
			// Table format has header row with "CLIENT TYPE" and data rows with client names
			lines := strings.Split(stdout, "\n")
			var foundClients []string
			inDataSection := false

			for _, line := range lines {
				line = strings.TrimSpace(line)
				// Skip empty lines and table borders
				if line == "" || strings.HasPrefix(line, "┌") || strings.HasPrefix(line, "└") || strings.HasPrefix(line, "├") {
					continue
				}

				// Skip the header row
				if strings.Contains(line, "CLIENT TYPE") {
					inDataSection = true
					continue
				}

				// Extract client names from data rows (format: "│ client-name │")
				if inDataSection && strings.HasPrefix(line, "│") && strings.HasSuffix(line, "│") {
					// Remove the table borders and trim whitespace
					client := strings.TrimSpace(strings.Trim(line, "│"))
					if client != "" {
						foundClients = append(foundClients, client)
					}
				}
			}

			// Verify all clients are present
			expectedClients := []string{"vscode", "cursor", "roo-code", "cline", "claude-code"}
			Expect(foundClients).To(HaveLen(len(expectedClients)), "Should find all registered clients")
			for _, expectedClient := range expectedClients {
				Expect(foundClients).To(ContainElement(MatchRegexp(fmt.Sprintf(".*%s.*", expectedClient))), "Should contain client: %s", expectedClient)
			}

			// Verify alphabetical order
			for i := 1; i < len(foundClients); i++ {
				Expect(foundClients[i-1] < foundClients[i]).To(BeTrue(),
					"Clients should be sorted alphabetically: %s should come before %s",
					foundClients[i-1], foundClients[i])
			}
		})
	})
})
