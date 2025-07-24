package e2e

import (
	"fmt"
	"os/exec"
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
			Expect(output).To(ContainSubstring("Remove all workloads in the specified group"))
		})

		It("should return error when group does not exist", func() {
			groupName := fmt.Sprintf("non-existent-group-%d", time.Now().UnixNano())
			_, stderr, err := NewTHVCommand(config, "rm", "--group", groupName).ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring(fmt.Sprintf("group '%s' does not exist", groupName)))
		})

		It("should return success when group exists but has no workloads", func() {
			// Create a group
			groupName := fmt.Sprintf("empty-group-%d", time.Now().UnixNano())
			createGroup(groupName)

			// Try to remove workloads from empty group
			stdout, stderr := NewTHVCommand(config, "rm", "--group", groupName).ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring(fmt.Sprintf("No workloads found in group '%s'", groupName)))
		})

		It("should remove single workload from group", func() {
			// Create a group
			groupName := fmt.Sprintf("single-workload-group-%d", time.Now().UnixNano())
			createGroup(groupName)

			// Create a workload in the group
			workloadName := fmt.Sprintf("test-workload-%d", time.Now().UnixNano())
			createWorkloadInGroup(workloadName, groupName)

			// Verify workload is running
			Expect(isWorkloadRunning(workloadName)).To(BeTrue())

			// Remove workloads from group
			stdout, stderr := NewTHVCommand(config, "rm", "--group", groupName).ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("Successfully removed 1 workload(s) from group"))

			// Verify workload is no longer running
			Expect(isWorkloadRunning(workloadName)).To(BeFalse())
		})

		It("should remove multiple workloads from group", func() {
			// Create a group
			groupName := fmt.Sprintf("multi-workload-group-%d", time.Now().UnixNano())
			createGroup(groupName)

			// Create multiple workloads in the group
			workload1 := fmt.Sprintf("test-workload-1-%d", time.Now().UnixNano())
			workload2 := fmt.Sprintf("test-workload-2-%d", time.Now().UnixNano())
			workload3 := fmt.Sprintf("test-workload-3-%d", time.Now().UnixNano())

			createWorkloadInGroup(workload1, groupName)
			createWorkloadInGroup(workload2, groupName)
			createWorkloadInGroup(workload3, groupName)

			// Verify workloads are running
			Expect(isWorkloadRunning(workload1)).To(BeTrue())
			Expect(isWorkloadRunning(workload2)).To(BeTrue())
			Expect(isWorkloadRunning(workload3)).To(BeTrue())

			// Remove workloads from group
			stdout, stderr := NewTHVCommand(config, "rm", "--group", groupName).ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("Successfully removed 3 workload(s) from group"))

			// Verify workloads are no longer running
			Expect(isWorkloadRunning(workload1)).To(BeFalse())
			Expect(isWorkloadRunning(workload2)).To(BeFalse())
			Expect(isWorkloadRunning(workload3)).To(BeFalse())
		})

		It("should handle mixed workloads (some in group, some not)", func() {
			// Create a group
			groupName := fmt.Sprintf("mixed-group-%d", time.Now().UnixNano())
			createGroup(groupName)

			// Create workloads in the group
			groupWorkload1 := fmt.Sprintf("group-workload-1-%d", time.Now().UnixNano())
			groupWorkload2 := fmt.Sprintf("group-workload-2-%d", time.Now().UnixNano())

			// Create workloads not in the group
			nonGroupWorkload1 := fmt.Sprintf("non-group-workload-1-%d", time.Now().UnixNano())
			nonGroupWorkload2 := fmt.Sprintf("non-group-workload-2-%d", time.Now().UnixNano())

			createWorkloadInGroup(groupWorkload1, groupName)
			createWorkloadInGroup(groupWorkload2, groupName)
			createWorkload(nonGroupWorkload1)
			createWorkload(nonGroupWorkload2)

			// Verify all workloads are running
			Expect(isWorkloadRunning(groupWorkload1)).To(BeTrue())
			Expect(isWorkloadRunning(groupWorkload2)).To(BeTrue())
			Expect(isWorkloadRunning(nonGroupWorkload1)).To(BeTrue())
			Expect(isWorkloadRunning(nonGroupWorkload2)).To(BeTrue())

			// Remove workloads from group
			stdout, stderr := NewTHVCommand(config, "rm", "--group", groupName).ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("Successfully removed 2 workload(s) from group"))

			// Verify only group workloads are removed
			Expect(isWorkloadRunning(groupWorkload1)).To(BeFalse())
			Expect(isWorkloadRunning(groupWorkload2)).To(BeFalse())
			Expect(isWorkloadRunning(nonGroupWorkload1)).To(BeTrue())
			Expect(isWorkloadRunning(nonGroupWorkload2)).To(BeTrue())

			// Clean up non-group workloads
			removeWorkload(nonGroupWorkload1)
			removeWorkload(nonGroupWorkload2)
		})

		It("should require group flag when no workload name provided", func() {
			_, stderr, err := NewTHVCommand(config, "rm").ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("workload name is required when not using --group flag"))
		})

		It("should work with normal rm command when workload name provided", func() {
			// Create a workload without group
			workloadName := fmt.Sprintf("test-workload-%d", time.Now().UnixNano())
			createWorkload(workloadName)

			// Verify workload is running
			Expect(isWorkloadRunning(workloadName)).To(BeTrue())

			// Remove workload using normal rm command
			stdout, stderr := NewTHVCommand(config, "rm", workloadName).ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("Container " + workloadName + " removed successfully"))

			// Verify workload is no longer running
			Expect(isWorkloadRunning(workloadName)).To(BeFalse())
		})
	})
})

// Helper functions

func createGroup(groupName string) {
	// We need to use exec.Command here since we don't have access to config in helper functions
	cmd := exec.Command("../../bin/thv", "group", "create", groupName)
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to create group: %s", string(output))
}

func createWorkloadInGroup(workloadName, groupName string) {
	// We need to use exec.Command here since we don't have access to config in helper functions
	cmd := exec.Command("../../bin/thv", "run", "fetch", "--group", groupName, "--name", workloadName)
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to create workload in group: %s", string(output))
}

func createWorkload(workloadName string) {
	// We need to use exec.Command here since we don't have access to config in helper functions
	cmd := exec.Command("../../bin/thv", "run", "fetch", "--name", workloadName)
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to create workload: %s", string(output))
}

func removeWorkload(workloadName string) {
	// We need to use exec.Command here since we don't have access to config in helper functions
	cmd := exec.Command("../../bin/thv", "rm", workloadName)
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to remove workload: %s", string(output))
}

func isWorkloadRunning(workloadName string) bool {
	// We need to use exec.Command here since we don't have access to config in helper functions
	cmd := exec.Command("../../bin/thv", "list", "--all")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), workloadName)
}

func cleanupTestGroups() {
	// This is a simplified cleanup - in a real scenario, you might want to be more specific
	// about which groups to clean up based on test naming conventions
}
