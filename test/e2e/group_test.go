package e2e_test

// TODO: add back in once we have a working group command
/*
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
	"github.com/stacklok/toolhive/test/e2e"
)

func init() {
	logger.Initialize()
}

var _ = Describe("Group", func() {
	var (
		config          *e2e.TestConfig
		groupName       string
		thvBinary       string
		sharedTimestamp int64
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		// Use a shared timestamp for all workload names in this test
		sharedTimestamp = time.Now().UnixNano()
		// Use a more unique group name to avoid conflicts between tests
		groupName = fmt.Sprintf("testgroup-e2e-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
		thvBinary = os.Getenv("THV_BINARY")
		if thvBinary == "" {
			Skip("THV_BINARY environment variable not set")
		}

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
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

	Describe("Creating groups", func() {
		Context("when creating a new group", func() {
			It("should successfully create the group", func() {
				By("Creating a group via CLI")
				cmd := exec.Command(thvBinary, "group", "create", groupName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to create group: %s", string(output))

				By("Verifying the group was created via manager")
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				exists, err := manager.Exists(ctx, groupName)
				Expect(err).ToNot(HaveOccurred())
				Expect(exists).To(BeTrue(), "Group should exist after creation")

				By("Verifying we can get the group")
				group, err := manager.Get(ctx, groupName)
				Expect(err).ToNot(HaveOccurred())
				Expect(group.Name).To(Equal(groupName))
			})
		})

		Context("when creating a duplicate group", func() {
			BeforeEach(func() {
				By("Creating the initial group")
				cmd := exec.Command(thvBinary, "group", "create", groupName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to create initial group: %s", string(output))
			})

			It("should fail when creating the same group again", func() {
				By("Attempting to create the same group again")
				cmd := exec.Command(thvBinary, "group", "create", groupName)
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail when creating duplicate group")
				Expect(string(output)).To(ContainSubstring("already exists"))
			})
		})

		Context("when providing invalid arguments", func() {
			It("should fail when no group name is provided", func() {
				By("Attempting to create a group without a name")
				cmd := exec.Command(thvBinary, "group", "create")
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail when no group name provided")
				Expect(string(output)).To(ContainSubstring("accepts 1 arg(s)"))
			})

			It("should fail with empty group name", func() {
				By("Attempting to create a group with empty name")
				cmd := exec.Command(thvBinary, "group", "create", "")
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail with empty group name")
				Expect(string(output)).To(ContainSubstring("group name cannot be empty"))
			})

			It("should fail with invalid characters in group name", func() {
				By("Attempting to create a group with invalid characters")
				cmd := exec.Command(thvBinary, "group", "create", "invalid/group/name")
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail with invalid group name")
				Expect(string(output)).To(ContainSubstring("failed to create file"))
			})
		})

		Context("when creating groups concurrently", func() {
			It("should handle concurrent creation gracefully", func() {
				By("Starting concurrent group creation")
				done := make(chan bool, 2)
				errors := make(chan error, 2)

				// First goroutine
				go func() {
					cmd := exec.Command(thvBinary, "group", "create", groupName)
					_, err := cmd.CombinedOutput()
					if err != nil {
						errors <- err
					}
					done <- true
				}()

				// Second goroutine (should fail)
				go func() {
					// Wait a bit to ensure the first one starts
					time.Sleep(100 * time.Millisecond)
					cmd := exec.Command(thvBinary, "group", "create", groupName)
					_, err := cmd.CombinedOutput()
					if err != nil {
						errors <- err
					}
					done <- true
				}()

				By("Waiting for both operations to complete")
				<-done
				<-done

				By("Verifying at least one concurrent creation failed")
				errorCount := len(errors)
				Expect(errorCount).To(BeNumerically(">=", 1), "At least one concurrent creation should fail")

				By("Verifying the group exists")
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				exists, err := manager.Exists(ctx, groupName)
				Expect(err).ToNot(HaveOccurred())
				Expect(exists).To(BeTrue(), "Group should exist after concurrent creation")
			})
		})
	})

	Describe("Running workloads with groups", func() {
		BeforeEach(func() {
			By("Creating a test group")
			cmd := exec.Command(thvBinary, "group", "create", groupName)
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to create test group: %s", string(output))
		})

		Context("when running a workload with a group", func() {
			It("should successfully add a workload from registry", func() {
				By("Adding a workload from registry")
				workloadName := fmt.Sprintf("test-workload-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				cmd := exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to add workload: %s", string(output))

				By("Verifying the workload was added to the group")
				// Add a delay to ensure the workload is fully registered
				time.Sleep(3 * time.Second)
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				workloadGroup, err := manager.GetWorkloadGroup(ctx, workloadName)
				Expect(err).ToNot(HaveOccurred())
				Expect(workloadGroup).ToNot(BeNil(), "Workload should be in a group")
				Expect(workloadGroup.Name).To(Equal(groupName), "Workload should be in the correct group")

				By("Verifying the workload appears in the list")
				// Add a small delay to ensure the workload is fully registered
				time.Sleep(2 * time.Second)
				listCmd := exec.Command(thvBinary, "list", "--all")
				listOutput, err := listCmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred())
				Expect(string(listOutput)).To(ContainSubstring(workloadName))
				Expect(string(listOutput)).To(ContainSubstring(groupName))
			})

			It("should successfully add a workload with custom flags", func() {
				By("Adding a workload with custom flags")
				workloadName := fmt.Sprintf("test-workload-flags-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				// Use a unique port to avoid conflicts
				uniquePort := fmt.Sprintf("%d", 9000+GinkgoRandomSeed()%1000)
				cmd := exec.Command(thvBinary, "run", "fetch",
					"--group", groupName,
					"--name", workloadName,
					"--transport", "sse",
					"--proxy-port", uniquePort,
					"--env", "TEST=value",
					"--label", "custom=label")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to add workload with flags: %s", string(output))

				By("Verifying the workload was added to the group")
				// Add a delay to ensure the workload is fully registered
				time.Sleep(3 * time.Second)
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				workloadGroup, err := manager.GetWorkloadGroup(ctx, workloadName)
				Expect(err).ToNot(HaveOccurred())
				Expect(workloadGroup).ToNot(BeNil(), "Workload should be in a group")
				Expect(workloadGroup.Name).To(Equal(groupName), "Workload should be in the correct group")
			})
		})

		Context("when running workloads with invalid arguments", func() {
			It("should fail when group does not exist", func() {
				By("Attempting to add workload to non-existent group")
				cmd := exec.Command(thvBinary, "run", "fetch", "--group", "nonexistent-group", "--name", "test-workload")
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail when group does not exist")
				Expect(string(output)).To(ContainSubstring("does not exist"))
			})

			It("should fail when server/image does not exist", func() {
				By("Attempting to add non-existent server")
				cmd := exec.Command(thvBinary, "run", "nonexistent-server", "--group", groupName, "--name", "test-workload")
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail when server does not exist")
				Expect(string(output)).To(ContainSubstring("image not found"))
			})
		})

		Context("when running workloads with group constraints", func() {
			var workloadName string

			BeforeEach(func() {
				workloadName = fmt.Sprintf("test-group-constraint-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				By("Adding initial workload")
				cmd := exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to add initial workload: %s", string(output))
			})

			It("should allow re-running the same workload in the same group", func() {
				By("Re-running the same workload in the same group")
				cmd := exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Should allow re-running workload in same group: %s", string(output))

				By("Verifying the workload is still in the correct group")
				time.Sleep(3 * time.Second)
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				workloadGroup, err := manager.GetWorkloadGroup(ctx, workloadName)
				Expect(err).ToNot(HaveOccurred())
				Expect(workloadGroup).ToNot(BeNil(), "Workload should still be in a group")
				Expect(workloadGroup.Name).To(Equal(groupName), "Workload should still be in the correct group")
			})
		})

		Context("when running workload with multiple groups", func() {
			var workloadName string
			var secondGroupName string

			BeforeEach(func() {
				workloadName = fmt.Sprintf("test-multi-group-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				secondGroupName = fmt.Sprintf("testgroup-e2e-second-%d-%d", GinkgoRandomSeed(), sharedTimestamp)

				By("Creating second group")
				cmd := exec.Command(thvBinary, "group", "create", secondGroupName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to create second group: %s", string(output))

				By("Adding workload to first group")
				cmd = exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
				output, err = cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to add workload to first group: %s", string(output))
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

			It("should fail when attempting to run workload in a different group", func() {
				By("Attempting to run workload in a different group")
				cmd := exec.Command(thvBinary, "run", "fetch", "--group", secondGroupName, "--name", workloadName)
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail when attempting to run workload in different group")
				Expect(string(output)).To(ContainSubstring("already in group"))
				Expect(string(output)).To(ContainSubstring(groupName), "Error should mention the original group name")
			})
		})

		Context("when running workload with invalid flags", func() {
			It("should fail with invalid port number", func() {
				By("Attempting to add workload with invalid port")
				workloadName := fmt.Sprintf("test-invalid-port-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				cmd := exec.Command(thvBinary, "run", "fetch",
					"--group", groupName,
					"--name", workloadName,
					"--proxy-port", "99999")
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail with invalid port")
				Expect(string(output)).To(ContainSubstring("not available"))
			})

			It("should fail with invalid transport", func() {
				By("Attempting to add workload with invalid transport")
				workloadName := fmt.Sprintf("test-invalid-transport-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				cmd := exec.Command(thvBinary, "run", "fetch",
					"--group", groupName,
					"--name", workloadName,
					"--transport", "invalid-transport")
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail with invalid transport")
				Expect(string(output)).To(ContainSubstring("invalid transport mode"))
			})
		})

		Context("when running workload with command arguments", func() {
			It("should successfully add workload with arguments after --", func() {
				By("Adding workload with arguments")
				workloadName := fmt.Sprintf("test-with-args-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				cmd := exec.Command(thvBinary, "run", "fetch",
					"--group", groupName,
					"--name", workloadName,
					"--", "--help")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to add workload with args: %s", string(output))

				By("Verifying the workload was added to the group")
				// Add a delay to ensure the workload is fully registered
				time.Sleep(3 * time.Second)
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				workloadGroup, err := manager.GetWorkloadGroup(ctx, workloadName)
				Expect(err).ToNot(HaveOccurred())
				Expect(workloadGroup).ToNot(BeNil(), "Workload should be in a group")
				Expect(workloadGroup.Name).To(Equal(groupName), "Workload should be in the correct group")
			})
		})
	})

	Describe("Group command help", func() {
		It("should display help for group command", func() {
			By("Getting group command help")
			cmd := exec.Command(thvBinary, "group", "--help")
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("manage logical groupings of MCP servers"))
			Expect(string(output)).To(ContainSubstring("create"))
		})

		It("should display help for group create command", func() {
			By("Getting group create command help")
			cmd := exec.Command(thvBinary, "group", "create", "--help")
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("Create a new logical group of MCP servers"))
		})

		It("should display help for run command with group flag", func() {
			By("Getting run command help")
			cmd := exec.Command(thvBinary, "run", "--help")
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("--group"))
			Expect(string(output)).To(ContainSubstring("--transport"))
			Expect(string(output)).To(ContainSubstring("--name"))
		})
	})

	Describe("Integration with list command", func() {
		BeforeEach(func() {
			By("Creating a test group")
			cmd := exec.Command(thvBinary, "group", "create", groupName)
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to create test group: %s", string(output))
		})

		It("should show workloads in groups when listing", func() {
			By("Adding a workload to the group")
			workloadName := fmt.Sprintf("test-list-integration-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
			cmd := exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to add workload: %s", string(output))

			By("Listing all workloads")
			// Add a longer delay to ensure the workload is fully registered
			time.Sleep(5 * time.Second)
			listCmd := exec.Command(thvBinary, "list", "--all")
			listOutput, err := listCmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred())

			By("Verifying the workload appears with group information")
			outputStr := string(listOutput)
			Expect(outputStr).To(ContainSubstring(workloadName))
			Expect(outputStr).To(ContainSubstring(groupName))

			// Check that the GROUP column is present
			lines := strings.Split(outputStr, "\n")
			headerFound := false
			for _, line := range lines {
				if strings.Contains(line, "GROUP") {
					headerFound = true
					break
				}
			}
			Expect(headerFound).To(BeTrue(), "GROUP column should be present in list output")
		})
	})
})
*/
