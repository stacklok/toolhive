package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/workloads"
	"github.com/stacklok/toolhive/test/e2e"
)

func init() {
	logger.Initialize()
}

var _ = Describe("Group", Label("core", "groups", "e2e"), func() {
	var (
		config           *e2e.TestConfig
		groupName        string
		sharedTimestamp  int64
		createdWorkloads []string
	)

	createWorkloadInGroup := func(workloadName, groupName string) {
		e2e.NewTHVCommand(config, "run", "fetch", "--group", groupName, "--name", workloadName).ExpectSuccess()
		createdWorkloads = append(createdWorkloads, workloadName)
		err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
		Expect(err).ToNot(HaveOccurred())
	}

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		// Use a shared timestamp for all workload names in this test
		sharedTimestamp = time.Now().UnixNano()
		// Use a more unique group name to avoid conflicts between tests
		groupName = fmt.Sprintf("testgroup-e2e-%d-%d", GinkgoRandomSeed(), sharedTimestamp)

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			for _, workloadName := range createdWorkloads {
				err := e2e.StopAndRemoveMCPServer(config, workloadName)
				Expect(err).NotTo(HaveOccurred(), "Should be able to stop and remove server")
			}

			err := e2e.RemoveGroup(config, groupName)
			Expect(err).NotTo(HaveOccurred(), "Should be able to remove group")
		}
	})

	Describe("Creating groups", func() {
		Context("when creating a new group", func() {
			It("should successfully create the group", func() {
				By("Creating a group via CLI")
				_, _ = e2e.NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()

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
				_, _ = e2e.NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()
			})

			It("should fail when creating the same group again", func() {
				By("Attempting to create the same group again")
				stdout, stderr, err := e2e.NewTHVCommand(config, "group", "create", groupName).ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail when creating duplicate group")
				Expect(stdout + stderr).To(ContainSubstring("already exists"))
			})
		})

		Context("when creating groups concurrently", func() {
			It("should handle concurrent creation gracefully", func() {
				By("Starting concurrent group creation")
				done := make(chan bool, 2)
				errors := make(chan error, 2)

				// First goroutine
				go func() {
					_, _, err := e2e.NewTHVCommand(config, "group", "create", groupName).Run()
					if err != nil {
						errors <- err
					}
					done <- true
				}()

				// Second goroutine (should fail)
				go func() {
					// Wait a bit to ensure the first one starts
					time.Sleep(100 * time.Millisecond)
					_, _, err := e2e.NewTHVCommand(config, "group", "create", groupName).Run()
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
			_, _ = e2e.NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()
		})

		Context("when running a workload with a group", func() {
			It("should successfully add a workload from registry", func() {
				By("Adding a workload from registry")
				workloadName := fmt.Sprintf("test-workload-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				createWorkloadInGroup(workloadName, groupName)

				workloadGroupName, err := getWorkloadGroup(workloadName)
				Expect(err).ToNot(HaveOccurred())
				Expect(workloadGroupName).To(Equal(groupName), "Workload should be in the correct group")

				By("Verifying the workload appears in the list")
				listOutput, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()
				Expect(listOutput).To(ContainSubstring(workloadName))
				Expect(listOutput).To(ContainSubstring(groupName))
			})
		})

		Context("when running workloads with invalid arguments", func() {
			It("should fail when group does not exist", func() {
				By("Attempting to add workload to non-existent group")
				workloadName := fmt.Sprintf("test-nonexistent-group-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				stdout, stderr, err := e2e.NewTHVCommand(config, "run", "fetch", "--group", "nonexistent-group", "--name", workloadName).ExpectFailure()
				Expect(err).To(HaveOccurred(), "Should fail when group does not exist")
				Expect(stdout + stderr).To(ContainSubstring("does not exist"))
			})
		})

		Context("when running workloads with group constraints", func() {
			var workloadName string

			BeforeEach(func() {
				workloadName = fmt.Sprintf("test-group-constraint-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				By("Creating a workload in the group")
				createWorkloadInGroup(workloadName, groupName)
			})

			It("should allow restarting the workload in the group", func() {
				By("Restarting the workload")
				_, _ = e2e.NewTHVCommand(config, "restart", workloadName).ExpectSuccess()

				By("Verifying the workload is still in the correct group")
				err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())

				workloadGroupName, err := getWorkloadGroup(workloadName)
				Expect(err).ToNot(HaveOccurred())
				Expect(workloadGroupName).To(Equal(groupName), "Workload should still be in the correct group")
			})

			It("should show workloads in groups when listing", func() {
				By("Listing all workloads")
				listOutput, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()

				By("Verifying the workload appears with group information")
				outputStr := listOutput
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
})

// getWorkloadGroup retrieves the group name for a workload using the workload manager
func getWorkloadGroup(workloadName string) (string, error) {
	ctx := context.Background()

	// Create a workload manager
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Get the workload details
	workload, err := manager.GetWorkload(ctx, workloadName)
	if err != nil {
		return "", fmt.Errorf("failed to get workload %s: %w", workloadName, err)
	}

	return workload.Group, nil
}
