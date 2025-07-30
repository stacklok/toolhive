package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	logger.Initialize()
}

var _ = Describe("List Group", func() {
	var (
		config          *testConfig
		groupName       string
		thvBinary       string
		sharedTimestamp int64
	)

	BeforeEach(func() {
		config = NewTestConfig()
		// Use a shared timestamp for all workload names in this test
		sharedTimestamp = time.Now().UnixNano()
		// Use a more unique group name to avoid conflicts between tests
		groupName = fmt.Sprintf("testgroup-list-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
		thvBinary = os.Getenv("THV_BINARY")
		if thvBinary == "" {
			Skip("THV_BINARY environment variable not set")
		}

		// Check if thv binary is available
		err := CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up the group if it exists
			cleanupManager, cleanupErr := groups.NewManager()
			if cleanupErr == nil {
				ctx := context.Background()
				_ = cleanupManager.Delete(ctx, groupName)
			}
		}
	})

	Describe("Basic group filtering", func() {
		BeforeEach(func() {
			By("Creating a test group")
			cmd := exec.Command(thvBinary, "group", "create", groupName)
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to create test group: %s", string(output))
		})

		Context("when listing workloads in an empty group", func() {
			It("should show no workloads found message", func() {
				By("Listing workloads in empty group")
				cmd := exec.Command(thvBinary, "list", "--group", groupName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				Expect(string(output)).To(ContainSubstring(fmt.Sprintf("No MCP servers found in group '%s'", groupName)))
			})
		})

		Context("when listing workloads in a group with workloads", func() {
			var workloadName string

			BeforeEach(func() {
				workloadName = fmt.Sprintf("test-workload-list-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				By("Adding a workload to the group")
				cmd := exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to add workload: %s", string(output))

				// Wait for workload to be fully registered and appear in the group
				time.Sleep(5 * time.Second)
			})

			It("should show only workloads in the specified group", func() {
				// Wait for workload to appear in the group
				By("Waiting for workload to appear in group")
				deadline := time.Now().Add(10 * time.Second)
				found := false
				for time.Now().Before(deadline) {
					cmd := exec.Command(thvBinary, "list", "--group", groupName)
					output, err := cmd.CombinedOutput()
					if err == nil && strings.Contains(string(output), workloadName) {
						found = true
						break
					}
					time.Sleep(500 * time.Millisecond)
				}
				Expect(found).To(BeTrue(), "Workload should appear in group within 10 seconds")

				By("Listing workloads in the group")
				cmd := exec.Command(thvBinary, "list", "--group", groupName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred())

				outputStr := string(output)
				Expect(outputStr).To(ContainSubstring(workloadName))
				Expect(outputStr).To(ContainSubstring(groupName))
				Expect(outputStr).To(ContainSubstring("NAME"))
				Expect(outputStr).To(ContainSubstring("GROUP"))

				// Verify it's the only workload shown
				lines := strings.Split(outputStr, "\n")
				workloadCount := 0
				for _, line := range lines {
					if strings.Contains(line, workloadName) {
						workloadCount++
					}
				}
				Expect(workloadCount).To(Equal(1), "Should show exactly one workload")
			})

			It("should not show workloads from other groups", func() {
				By("Listing all workloads")
				cmd := exec.Command(thvBinary, "list", "--all")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred())

				outputStr := string(output)
				Expect(outputStr).To(ContainSubstring(workloadName))

				By("Listing workloads in default group")
				cmd = exec.Command(thvBinary, "list", "--group", "default")
				output, err = cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred())

				outputStr = string(output)
				Expect(outputStr).ToNot(ContainSubstring(workloadName), "Should not show workload from different group")
			})
		})
	})

	Describe("Group filtering with different formats", func() {
		var workloadName string

		BeforeEach(func() {
			By("Creating a test group")
			cmd := exec.Command(thvBinary, "group", "create", groupName)
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to create test group: %s", string(output))

			workloadName = fmt.Sprintf("test-workload-format-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
			By("Adding a workload to the group")
			cmd = exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
			output, err = cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to add workload: %s", string(output))

			// Wait for workload to be fully registered
			time.Sleep(3 * time.Second)
		})

		Context("when using JSON format", func() {
			It("should output JSON with group information", func() {
				By("Listing workloads in group with JSON format")
				cmd := exec.Command(thvBinary, "list", "--group", groupName, "--format", "json")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred())

				outputStr := string(output)
				Expect(outputStr).To(ContainSubstring(workloadName))
				Expect(outputStr).To(ContainSubstring(groupName))
				Expect(outputStr).To(ContainSubstring(`"group"`))
				Expect(outputStr).To(ContainSubstring(`"name"`))
				Expect(outputStr).To(ContainSubstring(`"status"`))
			})
		})

		Context("when using mcpservers format", func() {
			It("should output mcpservers configuration", func() {
				By("Listing workloads in group with mcpservers format")
				cmd := exec.Command(thvBinary, "list", "--group", groupName, "--format", "mcpservers")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred())

				outputStr := string(output)
				Expect(outputStr).To(ContainSubstring(workloadName))
				Expect(outputStr).To(ContainSubstring(`"mcpServers"`))
				Expect(outputStr).To(ContainSubstring(`"url"`))
				Expect(outputStr).To(ContainSubstring(`"type"`))
			})
		})
	})

	Describe("Group filtering with other flags", func() {
		var workloadName string

		BeforeEach(func() {
			By("Creating a test group")
			cmd := exec.Command(thvBinary, "group", "create", groupName)
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to create test group: %s", string(output))

			workloadName = fmt.Sprintf("test-workload-flags-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
			By("Adding a workload to the group")
			cmd = exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName, "--label", "test=value")
			output, err = cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to add workload: %s", string(output))

			// Wait for workload to be fully registered
			time.Sleep(3 * time.Second)
		})

		Context("when combining with --all flag", func() {
			It("should show all workloads in group including stopped ones", func() {
				By("Listing all workloads in group")
				cmd := exec.Command(thvBinary, "list", "--group", groupName, "--all")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred())

				outputStr := string(output)
				Expect(outputStr).To(ContainSubstring(workloadName))
				Expect(outputStr).To(ContainSubstring(groupName))
			})
		})

		Context("when combining with label filtering", func() {
			It("should filter by both group and label", func() {
				By("Listing workloads in group with label filter")
				cmd := exec.Command(thvBinary, "list", "--group", groupName, "--label", "test=value")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred())

				outputStr := string(output)
				Expect(outputStr).To(ContainSubstring(workloadName))
				Expect(outputStr).To(ContainSubstring(groupName))

				By("Listing workloads in group with non-matching label")
				cmd = exec.Command(thvBinary, "list", "--group", groupName, "--label", "test=nonexistent")
				output, err = cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred())

				outputStr = string(output)
				Expect(outputStr).To(ContainSubstring(fmt.Sprintf("No MCP servers found in group '%s'", groupName)))
			})
		})
	})

	Describe("Error handling", func() {
		Context("when specifying non-existent group", func() {
			It("should return an error", func() {
				By("Listing workloads in non-existent group")
				cmd := exec.Command(thvBinary, "list", "--group", "nonexistent-group")
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail when group does not exist")
				Expect(string(output)).To(ContainSubstring("does not exist"))
			})
		})

		Context("when specifying empty group name", func() {
			It("should return workloads with no group assignment", func() {
				By("Listing workloads with empty group name")
				cmd := exec.Command(thvBinary, "list", "--group", "")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Empty group name should be valid")

				outputStr := string(output)
				// Should show workloads that have no group assignment
				// This is the expected behavior for empty group name
				Expect(outputStr).To(ContainSubstring("NAME"))
				Expect(outputStr).To(ContainSubstring("GROUP"))
			})
		})
	})

	Describe("Multiple workloads in different groups", func() {
		var secondGroupName string
		var workload1Name, workload2Name string

		BeforeEach(func() {
			secondGroupName = fmt.Sprintf("testgroup-list-second-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
			workload1Name = fmt.Sprintf("test-workload1-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
			workload2Name = fmt.Sprintf("test-workload2-%d-%d", GinkgoRandomSeed(), sharedTimestamp)

			By("Creating two test groups")
			cmd := exec.Command(thvBinary, "group", "create", groupName)
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to create first group: %s", string(output))

			cmd = exec.Command(thvBinary, "group", "create", secondGroupName)
			output, err = cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to create second group: %s", string(output))

			By("Adding workloads to different groups")
			cmd = exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workload1Name)
			output, err = cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to add workload to first group: %s", string(output))

			cmd = exec.Command(thvBinary, "run", "fetch", "--group", secondGroupName, "--name", workload2Name)
			output, err = cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to add workload to second group: %s", string(output))

			// Wait for workloads to be fully registered
			time.Sleep(3 * time.Second)
		})

		AfterEach(func() {
			if config.CleanupAfter {
				cleanupManager, cleanupErr := groups.NewManager()
				if cleanupErr == nil {
					ctx := context.Background()
					_ = cleanupManager.Delete(ctx, secondGroupName)
				}
			}
		})

		It("should correctly filter workloads by group", func() {
			By("Listing workloads in first group")
			cmd := exec.Command(thvBinary, "list", "--group", groupName)
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred())

			outputStr := string(output)
			Expect(outputStr).To(ContainSubstring(workload1Name))
			Expect(outputStr).ToNot(ContainSubstring(workload2Name))

			By("Listing workloads in second group")
			cmd = exec.Command(thvBinary, "list", "--group", secondGroupName)
			output, err = cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred())

			outputStr = string(output)
			Expect(outputStr).To(ContainSubstring(workload2Name))
			Expect(outputStr).ToNot(ContainSubstring(workload1Name))

			By("Listing all workloads")
			cmd = exec.Command(thvBinary, "list", "--all")
			output, err = cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred())

			outputStr = string(output)
			Expect(outputStr).To(ContainSubstring(workload1Name))
			Expect(outputStr).To(ContainSubstring(workload2Name))
		})
	})

	Describe("Help and documentation", func() {
		It("should show group flag in help", func() {
			By("Getting help for list command")
			cmd := exec.Command(thvBinary, "list", "--help")
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred())

			outputStr := string(output)
			Expect(outputStr).To(ContainSubstring("--group"))
			Expect(outputStr).To(ContainSubstring("Filter workloads by group"))
		})
	})
})
