package e2e

// TODO: add back in once we have a working group command, and update the docs
/*
import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Group RM E2E Tests", func() {
	var config *TestConfig

	BeforeEach(func() {
		config = NewTestConfig()

		// Check if thv binary is available
		err := CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Describe("thv group rm command", func() {
		It("should show help for group rm command", func() {
			stdout, stderr := NewTHVCommand(config, "group", "rm", "--help").ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("Remove a group and remove all MCP servers from it"))
			Expect(output).To(ContainSubstring("By default, this only removes the group membership from workloads without deleting them"))
			Expect(output).To(ContainSubstring("Use --with-workloads to also delete the workloads"))
			Expect(output).To(ContainSubstring("The command will show a warning and require user confirmation"))
			Expect(output).To(ContainSubstring("Usage:"))
			Expect(output).To(ContainSubstring("thv group rm [group-name]"))
		})

		It("should return error when group does not exist", func() {
			groupName := fmt.Sprintf("group-rm-non-existent-group-%d", time.Now().UnixNano())
			_, stderr, err := NewTHVCommand(config, "group", "rm", groupName).ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("does not exist"))
		})

		It("should cancel deletion when user does not confirm", func() {
			groupName := fmt.Sprintf("group-rm-cancel-group-%d", time.Now().UnixNano())
			createGroup(config, groupName)

			// Try to delete the group but provide 'n' for no
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName)
			cmd.Stdin = strings.NewReader("n\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("Group deletion cancelled."))

			// Verify group still exists
			stdout, _ := NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(groupName))
		})

		It("should delete empty group successfully", func() {
			// Create a group
			groupName := fmt.Sprintf("group-rm-empty-group-%d", time.Now().UnixNano())
			createGroup(config, groupName)

			// Verify group exists
			stdout, _ := NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(groupName))

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))
			Expect(string(output)).To(ContainSubstring(fmt.Sprintf("Group '%s' deleted successfully", groupName)))

			// Verify group is deleted
			stdout, _ = NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})

		It("should delete group with single workload", func() {
			// Create a group
			groupName := fmt.Sprintf("group-rm-single-workload-group-%d", time.Now().UnixNano())
			createGroup(config, groupName)

			// Create a workload in the group
			workloadName := fmt.Sprintf("group-rm-test-workload-%d", time.Now().UnixNano())
			createWorkloadInGroup(config, workloadName, groupName)

			// Verify the workload is running
			Expect(waitForWorkload(config, workloadName)).To(BeTrue(), "Workload did not appear in thv list within 3 seconds")

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))
			Expect(string(output)).To(ContainSubstring("Removed 1 workload(s) from group"))
			Expect(string(output)).To(ContainSubstring(fmt.Sprintf("Group '%s' deleted successfully", groupName)))

			// Verify workload still exists (not deleted by default)
			stdout, _ := NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(workloadName))

			// Verify group is deleted
			stdout, _ = NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})

		It("should delete group with multiple workloads", func() {
			// Create a group
			groupName := fmt.Sprintf("group-rm-multi-workload-group-%d", time.Now().UnixNano())
			createGroup(config, groupName)

			// Create multiple workloads in the group
			workload1 := fmt.Sprintf("group-rm-test-workload-1-%d", time.Now().UnixNano())
			workload2 := fmt.Sprintf("group-rm-test-workload-2-%d", time.Now().UnixNano())
			workload3 := fmt.Sprintf("group-rm-test-workload-3-%d", time.Now().UnixNano())

			createWorkloadInGroup(config, workload1, groupName)
			createWorkloadInGroup(config, workload2, groupName)
			createWorkloadInGroup(config, workload3, groupName)

			// Verify all workloads are running
			for _, workloadName := range []string{workload1, workload2, workload3} {
				Expect(waitForWorkload(config, workloadName)).To(BeTrue(), "Workload %s did not appear in thv list within 3 seconds", workloadName)
			}

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))
			Expect(string(output)).To(ContainSubstring("Removed 3 workload(s) from group"))
			Expect(string(output)).To(ContainSubstring(fmt.Sprintf("Group '%s' deleted successfully", groupName)))

			// Verify workloads still exist (not deleted by default)
			stdout, _ := NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(workload1))
			Expect(stdout).To(ContainSubstring(workload2))
			Expect(stdout).To(ContainSubstring(workload3))

			// Verify group is deleted
			stdout, _ = NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})

		It("should handle mixed workloads (some in group, some not)", func() {
			// Create a group
			groupName := fmt.Sprintf("group-rm-mixed-group-%d", time.Now().UnixNano())
			createGroup(config, groupName)

			// Create workloads in the group
			groupWorkload1 := fmt.Sprintf("group-rm-group-workload-1-%d", time.Now().UnixNano())
			groupWorkload2 := fmt.Sprintf("group-rm-group-workload-2-%d", time.Now().UnixNano())

			// Create workloads not in the group
			nonGroupWorkload1 := fmt.Sprintf("group-rm-non-group-workload-1-%d", time.Now().UnixNano())
			nonGroupWorkload2 := fmt.Sprintf("group-rm-non-group-workload-2-%d", time.Now().UnixNano())

			createWorkloadInGroup(config, groupWorkload1, groupName)
			createWorkloadInGroup(config, groupWorkload2, groupName)
			createWorkload(config, nonGroupWorkload1)
			createWorkload(config, nonGroupWorkload2)

			// Verify all workloads are running
			for _, workloadName := range []string{groupWorkload1, groupWorkload2, nonGroupWorkload1, nonGroupWorkload2} {
				Expect(waitForWorkload(config, workloadName)).To(BeTrue(), "Workload %s did not appear in thv list within 3 seconds", workloadName)
			}

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))
			Expect(string(output)).To(ContainSubstring("Removed 2 workload(s) from group"))
			Expect(string(output)).To(ContainSubstring(fmt.Sprintf("Group '%s' deleted successfully", groupName)))

			// Verify group workloads still exist (not deleted by default)
			stdout, _ := NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(groupWorkload1))
			Expect(stdout).To(ContainSubstring(groupWorkload2))

			// Verify non-group workloads are still running
			Expect(isWorkloadRunning(config, nonGroupWorkload1)).To(BeTrue(), "Non-group workload %s is not running", nonGroupWorkload1)
			Expect(isWorkloadRunning(config, nonGroupWorkload2)).To(BeTrue(), "Non-group workload %s is not running", nonGroupWorkload2)

			// Clean up non-group workloads
			removeWorkload(config, nonGroupWorkload1)
			removeWorkload(config, nonGroupWorkload2)

			// Verify group is deleted
			stdout, _ = NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})

		It("should delete group and workloads with --with-workloads flag", func() {
			// Create a group
			groupName := fmt.Sprintf("group-rm-with-workloads-group-%d", time.Now().UnixNano())
			createGroup(config, groupName)

			// Create multiple workloads in the group
			workload1 := fmt.Sprintf("group-rm-with-workloads-1-%d", time.Now().UnixNano())
			workload2 := fmt.Sprintf("group-rm-with-workloads-2-%d", time.Now().UnixNano())

			createWorkloadInGroup(config, workload1, groupName)
			createWorkloadInGroup(config, workload2, groupName)

			// Verify all workloads are running
			for _, workloadName := range []string{workload1, workload2} {
				Expect(waitForWorkload(config, workloadName)).To(BeTrue(), "Workload %s did not appear in thv list within 3 seconds", workloadName)
			}

			// Delete the group with --with-workloads flag (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName, "--with-workloads")
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))
			Expect(string(output)).To(ContainSubstring("Deleted 2 workload(s) from group"))
			Expect(string(output)).To(ContainSubstring(fmt.Sprintf("Group '%s' deleted successfully", groupName)))

			// Verify workloads are deleted
			stdout, _ := NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(workload1))
			Expect(stdout).NotTo(ContainSubstring(workload2))

			// Verify group is deleted
			stdout, _ = NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})
	})
})
*/
