package e2e_test

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/test/e2e"
)

func init() {
	logger.Initialize()
}

var _ = Describe("List Group", Label("core", "groups", "e2e"), func() {
	var (
		config          *e2e.TestConfig
		groupName       string
		sharedTimestamp int64
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		// Use a shared timestamp for all workload names in this test
		sharedTimestamp = time.Now().UnixNano()
		// Use a more unique group name to avoid conflicts between tests
		groupName = fmt.Sprintf("testgroup-list-%d-%d", GinkgoRandomSeed(), sharedTimestamp)

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Describe("Basic group filtering", func() {
		BeforeEach(func() {
			By("Creating a test group")
			_, _ = e2e.NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()
		})

		AfterEach(func() {
			if config.CleanupAfter {
				err := e2e.RemoveGroup(config, groupName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to remove group")
			}
		})

		Context("when listing workloads in an empty group", func() {
			It("should show no workloads found message", func() {
				By("Listing workloads in empty group")
				stdout, _ := e2e.NewTHVCommand(config, "list", "--group", groupName).ExpectSuccess()
				Expect(stdout).To(ContainSubstring(fmt.Sprintf("No MCP servers found in group '%s'", groupName)))
			})
		})

		Context("when listing workloads in a group with workloads", func() {
			var workloadName string

			BeforeEach(func() {
				workloadName = fmt.Sprintf("test-workload-list-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
				By("Adding a workload to the group")
				_, _ = e2e.NewTHVCommand(config, "run", "fetch", "--group", groupName, "--name", workloadName).ExpectSuccess()

				// Wait for workload to be fully registered
				err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				// Clean up the workload after each test
				if config.CleanupAfter {
					err := e2e.StopAndRemoveMCPServer(config, workloadName)
					Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove workload")
				}
			})

			It("should show only workloads in the specified group", func() {
				By("Listing workloads in the group")
				stdout, _ := e2e.NewTHVCommand(config, "list", "--group", groupName).ExpectSuccess()

				outputStr := stdout
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
				stdout, _ := e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()

				outputStr := stdout
				Expect(outputStr).To(ContainSubstring(workloadName))

				By("Listing workloads in default group")
				stdout, _ = e2e.NewTHVCommand(config, "list", "--group", "default").ExpectSuccess()

				outputStr = stdout
				Expect(outputStr).ToNot(ContainSubstring(workloadName), "Should not show workload from different group")
			})
		})
	})

	Describe("Group filtering with other flags", func() {
		var workloadName string

		BeforeEach(func() {
			By("Creating a test group")
			_, _ = e2e.NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()

			workloadName = fmt.Sprintf("test-workload-flags-%d-%d", GinkgoRandomSeed(), sharedTimestamp)
			By("Adding a workload to the group")
			_, _ = e2e.NewTHVCommand(config, "run", "fetch", "--group", groupName, "--name", workloadName, "--label", "test=value").ExpectSuccess()

			// Wait for workload to be fully registered
			err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() {
			if config.CleanupAfter {
				err := e2e.RemoveGroup(config, groupName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to remove group")

				err = e2e.StopAndRemoveMCPServer(config, workloadName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove workload")
			}
		})

		Context("when combining with --all flag", func() {
			It("should show all workloads in group including stopped ones", func() {
				By("Listing all workloads in group")
				stdout, _ := e2e.NewTHVCommand(config, "list", "--group", groupName, "--all").ExpectSuccess()

				outputStr := stdout
				Expect(outputStr).To(ContainSubstring(workloadName))
				Expect(outputStr).To(ContainSubstring(groupName))
			})
		})

		Context("when combining with label filtering", func() {
			It("should filter by both group and label", func() {
				By("Listing workloads in group with label filter")
				stdout, _ := e2e.NewTHVCommand(config, "list", "--group", groupName, "--label", "test=value").ExpectSuccess()

				outputStr := stdout
				Expect(outputStr).To(ContainSubstring(workloadName))
				Expect(outputStr).To(ContainSubstring(groupName))

				By("Listing workloads in group with non-matching label")
				stdout, _ = e2e.NewTHVCommand(config, "list", "--group", groupName, "--label", "test=nonexistent").ExpectSuccess()

				outputStr = stdout
				Expect(outputStr).To(ContainSubstring(fmt.Sprintf("No MCP servers found in group '%s'", groupName)))
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
			_, _ = e2e.NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()

			_, _ = e2e.NewTHVCommand(config, "group", "create", secondGroupName).ExpectSuccess()

			By("Adding workloads to different groups")
			_, _ = e2e.NewTHVCommand(config, "run", "fetch", "--group", groupName, "--name", workload1Name).ExpectSuccess()

			_, _ = e2e.NewTHVCommand(config, "run", "fetch", "--group", secondGroupName, "--name", workload2Name).ExpectSuccess()

			// Wait for workloads to be fully registered
			err := e2e.WaitForMCPServer(config, workload1Name, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())
			err = e2e.WaitForMCPServer(config, workload2Name, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() {
			if config.CleanupAfter {
				err := e2e.StopAndRemoveMCPServer(config, workload1Name)
				Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove first workload")
				err = e2e.StopAndRemoveMCPServer(config, workload2Name)
				Expect(err).ToNot(HaveOccurred(), "Should be able to stop and remove second workload")

				err = e2e.RemoveGroup(config, groupName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to remove group")
				err = e2e.RemoveGroup(config, secondGroupName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to remove second group")
			}
		})

		It("should correctly filter workloads by group", func() {
			By("Listing workloads in first group")
			stdout, _ := e2e.NewTHVCommand(config, "list", "--group", groupName).ExpectSuccess()

			outputStr := stdout
			Expect(outputStr).To(ContainSubstring(workload1Name))
			Expect(outputStr).ToNot(ContainSubstring(workload2Name))

			By("Listing workloads in second group")
			stdout, _ = e2e.NewTHVCommand(config, "list", "--group", secondGroupName).ExpectSuccess()

			outputStr = stdout
			Expect(outputStr).To(ContainSubstring(workload2Name))
			Expect(outputStr).ToNot(ContainSubstring(workload1Name))

			By("Listing all workloads")
			stdout, _ = e2e.NewTHVCommand(config, "list", "--all").ExpectSuccess()

			outputStr = stdout
			Expect(outputStr).To(ContainSubstring(workload1Name))
			Expect(outputStr).To(ContainSubstring(workload2Name))
		})
	})
})
