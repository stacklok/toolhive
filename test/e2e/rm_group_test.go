package e2e

// TODO: add back in once we have a working group command, and update the docs
/*
import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Group Remove E2E Tests", func() {
	var config *TestConfig

	BeforeEach(func() {
		config = NewTestConfig()

		// Check if thv binary is available
		err := CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		// Clean up any existing test groups and workloads
		cleanupTestGroups()
	})

	AfterEach(func() {
		// Clean up after each test
		cleanupTestGroups()
	})

	Describe("thv rm --group command", func() {
		It("should show help for rm command with group flag", func() {
			stdout, stderr := NewTHVCommand(config, "rm", "--help").ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("--group string"))
			Expect(output).To(ContainSubstring("Delete all workloads in the specified group"))
		})

		It("should return error when group does not exist", func() {
			groupName := fmt.Sprintf("rm-non-existent-group-%d", time.Now().UnixNano())
			_, stderr, err := NewTHVCommand(config, "rm", "--group", groupName).ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("does not exist"))
		})

		It("should return success when group exists but has no workloads", func() {
			groupName := fmt.Sprintf("rm-empty-group-%d", time.Now().UnixNano())
			createGroup(config, groupName)

			stdout, stderr := NewTHVCommand(config, "rm", "--group", groupName).ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("No workloads found in group"))
		})

		It("should remove single workload from group", func() {
			groupName := fmt.Sprintf("rm-group-single-%d", time.Now().UnixNano())
			createGroup(config, groupName)
			workloadName := fmt.Sprintf("rm-group-workload-%d", time.Now().UnixNano())
			createWorkloadInGroup(config, workloadName, groupName)

			// Verify the workload is running
			Expect(waitForWorkload(config, workloadName)).To(BeTrue(), "Workload did not appear in thv list within 3 seconds")

			// Remove the workload using --group flag
			NewTHVCommand(config, "rm", "--group", groupName).ExpectSuccess()

			// Verify workload is deleted
			Expect(isWorkloadRunning(config, workloadName)).To(BeFalse())
		})

		It("should remove multiple workloads from group", func() {
			groupName := fmt.Sprintf("rm-group-multi-%d", time.Now().UnixNano())
			createGroup(config, groupName)
			workload1 := fmt.Sprintf("rm-group-workload-1-%d", time.Now().UnixNano())
			workload2 := fmt.Sprintf("rm-group-workload-2-%d", time.Now().UnixNano())
			workload3 := fmt.Sprintf("rm-group-workload-3-%d", time.Now().UnixNano())
			createWorkloadInGroup(config, workload1, groupName)
			createWorkloadInGroup(config, workload2, groupName)
			createWorkloadInGroup(config, workload3, groupName)

			// Verify all workloads are running
			for _, workloadName := range []string{workload1, workload2, workload3} {
				Expect(waitForWorkload(config, workloadName)).To(BeTrue(), "Workload %s did not appear in thv list within 3 seconds", workloadName)
			}

			// Remove all workloads in the group
			stdout, stderr := NewTHVCommand(config, "rm", "--group", groupName).ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("Successfully removed 3 workload(s) from group"))

			// Verify workloads are deleted
			stdout, _ = NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(workload1))
			Expect(stdout).NotTo(ContainSubstring(workload2))
			Expect(stdout).NotTo(ContainSubstring(workload3))
		})

		It("should handle mixed workloads (some in group, some not)", func() {
			groupName := fmt.Sprintf("rm-group-mixed-%d", time.Now().UnixNano())
			createGroup(config, groupName)
			groupWorkload1 := fmt.Sprintf("rm-group-workload-1-%d", time.Now().UnixNano())
			groupWorkload2 := fmt.Sprintf("rm-group-workload-2-%d", time.Now().UnixNano())
			nonGroupWorkload1 := fmt.Sprintf("rm-non-group-workload-1-%d", time.Now().UnixNano())
			nonGroupWorkload2 := fmt.Sprintf("rm-non-group-workload-2-%d", time.Now().UnixNano())
			createWorkloadInGroup(config, groupWorkload1, groupName)
			createWorkloadInGroup(config, groupWorkload2, groupName)
			createWorkload(config, nonGroupWorkload1)
			createWorkload(config, nonGroupWorkload2)

			// Wait for the workloads to appear in thv list (up to 5 seconds)
			for _, workloadName := range []string{groupWorkload1, groupWorkload2, nonGroupWorkload1, nonGroupWorkload2} {
				Expect(waitForWorkload(config, workloadName)).To(BeTrue(), "Workload %s did not appear in thv list within 3 seconds", workloadName)
			}

			// Remove all workloads in the group
			stdout, stderr := NewTHVCommand(config, "rm", "--group", groupName).ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("Successfully removed 2 workload(s) from group"))

			// Verify only group workloads are deleted
			stdout, _ = NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupWorkload1))
			Expect(stdout).NotTo(ContainSubstring(groupWorkload2))
			Expect(stdout).To(ContainSubstring(nonGroupWorkload1))
			Expect(stdout).To(ContainSubstring(nonGroupWorkload2))

			// Clean up non-group workloads
			removeWorkload(config, nonGroupWorkload1)
			removeWorkload(config, nonGroupWorkload2)
		})

		It("should require group flag when no workload name provided", func() {
			_, stderr, err := NewTHVCommand(config, "rm").ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("workload name is required when not using --group flag"))
		})

		It("should work with normal rm command when workload name provided", func() {
			// Create a workload without group
			workloadName := fmt.Sprintf("rm-test-workload-%d", time.Now().UnixNano())
			createWorkload(config, workloadName)

			// Wait for the workload to appear in thv list (up to 5 seconds)
			Expect(waitForWorkload(config, workloadName)).To(BeTrue(), "Workload did not appear in thv list in time")

			// Remove workload using normal rm command
			NewTHVCommand(config, "rm", workloadName).ExpectSuccess()

			// Verify workload is no longer running
			Expect(isWorkloadRunning(config, workloadName)).To(BeFalse())
		})
	})
})

// Helper functions

func createGroup(config *TestConfig, groupName string) {
	NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()
}

func createWorkloadInGroup(config *TestConfig, workloadName, groupName string) {
	NewTHVCommand(config, "run", "fetch", "--group", groupName, "--name", workloadName).ExpectSuccess()
}

func createWorkload(config *TestConfig, workloadName string) {
	NewTHVCommand(config, "run", "fetch", "--name", workloadName).ExpectSuccess()
}

func removeWorkload(config *TestConfig, workloadName string) {
	NewTHVCommand(config, "rm", workloadName).ExpectSuccess()
}

func isWorkloadRunning(config *TestConfig, workloadName string) bool {
	stdout, _ := NewTHVCommand(config, "list", "--all").ExpectSuccess()
	return strings.Contains(stdout, workloadName)
}

func waitForWorkload(config *TestConfig, workloadName string) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if isWorkloadRunning(config, workloadName) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func cleanupTestGroups() {
	// This is a simplified cleanup - in a real scenario, you might want to be more specific
	// about which groups to clean up based on test naming conventions
}
*/
