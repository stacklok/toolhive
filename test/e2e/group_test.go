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
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Group", func() {
	var (
		config    *e2e.TestConfig
		groupName string
		thvBinary string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		groupName = "testgroup-e2e-" + fmt.Sprintf("%d", GinkgoRandomSeed())
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
				Expect(group.Workloads).To(BeEmpty(), "New group should have no workloads")
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

				By("Verifying exactly one concurrent creation failed")
				errorCount := len(errors)
				Expect(errorCount).To(Equal(1), "Exactly one concurrent creation should fail")

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
				workloadName := "test-workload-" + fmt.Sprintf("%d", GinkgoRandomSeed())
				cmd := exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to add workload: %s", string(output))

				By("Verifying the workload was added to the group")
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				group, err := manager.Get(ctx, groupName)
				Expect(err).ToNot(HaveOccurred())
				Expect(group.HasWorkload(workloadName)).To(BeTrue(), "Workload should be in group")

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
				workloadName := "test-workload-flags-" + fmt.Sprintf("%d", GinkgoRandomSeed())
				cmd := exec.Command(thvBinary, "run", "fetch",
					"--group", groupName,
					"--name", workloadName,
					"--transport", "sse",
					"--proxy-port", "8081",
					"--env", "TEST=value",
					"--label", "custom=label")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to add workload with flags: %s", string(output))

				By("Verifying the workload was added to the group")
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				group, err := manager.Get(ctx, groupName)
				Expect(err).ToNot(HaveOccurred())
				Expect(group.HasWorkload(workloadName)).To(BeTrue(), "Workload should be in group")
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

		Context("when running duplicate workloads", func() {
			var workloadName string

			BeforeEach(func() {
				workloadName = "test-duplicate-" + fmt.Sprintf("%d", GinkgoRandomSeed())
				By("Adding initial workload")
				cmd := exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to add initial workload: %s", string(output))
			})

			It("should fail when adding the same workload again", func() {
				By("Attempting to add the same workload again")
				cmd := exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail when adding duplicate workload")
				Expect(string(output)).To(ContainSubstring("already in group"))
			})
		})

		Context("when running workload with multiple groups", func() {
			var workloadName string
			var secondGroupName string

			BeforeEach(func() {
				workloadName = "test-multi-group-" + fmt.Sprintf("%d", GinkgoRandomSeed())
				secondGroupName = "testgroup-e2e-second-" + fmt.Sprintf("%d", GinkgoRandomSeed())

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

			It("should fail when adding workload to second group", func() {
				By("Attempting to add workload to second group")
				cmd := exec.Command(thvBinary, "run", "fetch", "--group", secondGroupName, "--name", workloadName)
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail when adding workload to second group")
				Expect(string(output)).To(ContainSubstring("already in group"))
			})
		})

		Context("when running workload with invalid flags", func() {
			It("should fail with invalid port number", func() {
				By("Attempting to add workload with invalid port")
				workloadName := "test-invalid-port-" + fmt.Sprintf("%d", GinkgoRandomSeed())
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
				workloadName := "test-invalid-transport-" + fmt.Sprintf("%d", GinkgoRandomSeed())
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
				workloadName := "test-with-args-" + fmt.Sprintf("%d", GinkgoRandomSeed())
				cmd := exec.Command(thvBinary, "run", "fetch",
					"--group", groupName,
					"--name", workloadName,
					"--", "--help")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to add workload with args: %s", string(output))

				By("Verifying the workload was added to the group")
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				group, err := manager.Get(ctx, groupName)
				Expect(err).ToNot(HaveOccurred())
				Expect(group.HasWorkload(workloadName)).To(BeTrue(), "Workload should be in group")
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
			workloadName := "test-list-integration-" + fmt.Sprintf("%d", GinkgoRandomSeed())
			cmd := exec.Command(thvBinary, "run", "fetch", "--group", groupName, "--name", workloadName)
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Failed to add workload: %s", string(output))

			By("Listing all workloads")
			// Add a small delay to ensure the workload is fully registered
			time.Sleep(2 * time.Second)
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
