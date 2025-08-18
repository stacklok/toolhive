package e2e_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Group Remove E2E Tests", func() {
	var (
		config           *e2e.TestConfig
		testGroupName    string
		secondGroupName  string
		createdWorkloads []string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		testGroupName = fmt.Sprintf("rm-test-group-%d-%d", GinkgoRandomSeed(), time.Now().UnixNano())
		secondGroupName = fmt.Sprintf("rm-test-group-2-%d-%d", GinkgoRandomSeed(), time.Now().UnixNano())
		createdWorkloads = []string{}

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		// Create test group
		e2e.NewTHVCommand(config, "group", "create", testGroupName).ExpectSuccess()
		e2e.NewTHVCommand(config, "group", "create", secondGroupName).ExpectSuccess()
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up workloads first
			for _, workloadName := range createdWorkloads {
				err := e2e.StopAndRemoveMCPServer(config, workloadName)
				Expect(err).NotTo(HaveOccurred(), "Should be able to stop and remove server")
			}

			// Clean up test groups
			err := e2e.RemoveGroup(config, testGroupName)
			Expect(err).NotTo(HaveOccurred(), "Should be able to remove group")
			err = e2e.RemoveGroup(config, secondGroupName)
			Expect(err).NotTo(HaveOccurred(), "Should be able to remove second group")
		}
	})

	createWorkloadInGroup := func(workloadName, groupName string) {
		e2e.NewTHVCommand(config, "run", "fetch", "--group", groupName, "--name", workloadName).ExpectSuccess()
		createdWorkloads = append(createdWorkloads, workloadName)
	}

	Describe("thv rm --group command", func() {
		It("should return error when group does not exist", func() {
			groupName := fmt.Sprintf("rm-non-existent-group-%d", GinkgoRandomSeed())
			_, stderr, err := e2e.NewTHVCommand(config, "rm", "--group", groupName).ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("does not exist"))
		})

		It("should return success when group exists but has no workloads", func() {
			stdout, stderr := e2e.NewTHVCommand(config, "rm", "--group", testGroupName).ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("No workloads found in group"))
		})

		It("should remove workloads from group", func() {
			groupWorkload1 := fmt.Sprintf("rm-group-workload-1-%d", GinkgoRandomSeed())
			groupWorkload2 := fmt.Sprintf("rm-group-workload-2-%d", GinkgoRandomSeed())
			nonGroupWorkload1 := fmt.Sprintf("rm-non-group-workload-1-%d", GinkgoRandomSeed())
			nonGroupWorkload2 := fmt.Sprintf("rm-non-group-workload-2-%d", GinkgoRandomSeed())
			createWorkloadInGroup(groupWorkload1, testGroupName)
			createWorkloadInGroup(groupWorkload2, testGroupName)
			createWorkloadInGroup(nonGroupWorkload1, secondGroupName)
			createWorkloadInGroup(nonGroupWorkload2, secondGroupName)

			// Wait for the workloads to appear in thv list
			for _, workloadName := range []string{groupWorkload1, groupWorkload2, nonGroupWorkload1, nonGroupWorkload2} {
				err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
				Expect(err).NotTo(HaveOccurred())
			}

			// Remove all workloads in the group
			stdout, stderr := e2e.NewTHVCommand(config, "rm", "--group", testGroupName).ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("Successfully removed 2 workload(s) from group"))

			// Verify only group workloads are deleted
			stdout, _ = e2e.NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupWorkload1))
			Expect(stdout).NotTo(ContainSubstring(groupWorkload2))
			Expect(stdout).To(ContainSubstring(nonGroupWorkload1))
			Expect(stdout).To(ContainSubstring(nonGroupWorkload2))
		})

		It("should require group flag when no workload name provided", func() {
			_, stderr, err := e2e.NewTHVCommand(config, "rm").ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("workload name is required when not using --group flag"))
		})
	})
})
