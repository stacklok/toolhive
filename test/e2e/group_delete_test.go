package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Group Delete E2E Tests", func() {
	var config *TestConfig

	BeforeEach(func() {
		config = NewTestConfig()

		// Check if thv binary is available
		err := CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	Describe("thv group delete command", func() {
		It("should show help for group delete command", func() {
			stdout, stderr := NewTHVCommand(config, "group", "delete", "--help").ExpectSuccess()
			output := stdout + stderr
			Expect(output).To(ContainSubstring("Delete a group and remove all MCP servers from it"))
			Expect(output).To(ContainSubstring("By default, this only removes the group membership from workloads without deleting them"))
			Expect(output).To(ContainSubstring("Use --with-workloads to also delete the workloads"))
			Expect(output).To(ContainSubstring("The command will show a warning and require user confirmation"))
			Expect(output).To(ContainSubstring("Usage:"))
			Expect(output).To(ContainSubstring("thv group delete [group-name]"))
		})

		It("should return error when group does not exist", func() {
			groupName := fmt.Sprintf("non-existent-group-%d", time.Now().UnixNano())
			_, stderr, err := NewTHVCommand(config, "group", "delete", groupName).ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring(fmt.Sprintf("group '%s' does not exist", groupName)))
		})

		It("should cancel deletion when user does not confirm", func() {
			// Create a group
			groupName := fmt.Sprintf("cancel-group-%d", time.Now().UnixNano())
			createGroup(groupName)

			// Verify group exists
			stdout, _ := NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(groupName))

			// Try to delete the group but cancel - use exec.Command for interactive input
			cmd := exec.Command(config.THVBinary, "group", "delete", groupName)
			cmd.Stdin = strings.NewReader("n\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))
			Expect(string(output)).To(ContainSubstring("Group deletion cancelled."))

			// Verify group still exists
			stdout, _ = NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(groupName))

			// Clean up by actually deleting the group
			cmd = exec.Command(config.THVBinary, "group", "delete", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			_, err = cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
		})

		It("should delete empty group successfully", func() {
			// Create a group
			groupName := fmt.Sprintf("empty-group-%d", time.Now().UnixNano())
			createGroup(groupName)

			// Verify group exists
			stdout, _ := NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(groupName))

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "delete", groupName)
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
			groupName := fmt.Sprintf("single-workload-group-%d", time.Now().UnixNano())
			createGroup(groupName)

			// Create a workload in the group
			workloadName := fmt.Sprintf("test-workload-%d", time.Now().UnixNano())
			createWorkloadInGroup(workloadName, groupName)

			// Verify workload is running
			dockerCmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", workloadName))
			output, err := dockerCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(workloadName))

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "delete", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err = cmd.CombinedOutput()
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
			dockerCmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", workload1))
			output, err := dockerCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(workload1))

			dockerCmd = exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", workload2))
			output, err = dockerCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(workload2))

			dockerCmd = exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", workload3))
			output, err = dockerCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(workload3))

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "delete", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err = cmd.CombinedOutput()
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
			dockerCmd := exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", groupWorkload1))
			output, err := dockerCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(groupWorkload1))

			dockerCmd = exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", groupWorkload2))
			output, err = dockerCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(groupWorkload2))

			dockerCmd = exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", nonGroupWorkload1))
			output, err = dockerCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(nonGroupWorkload1))

			dockerCmd = exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", nonGroupWorkload2))
			output, err = dockerCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(nonGroupWorkload2))

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "delete", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err = cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))
			Expect(string(output)).To(ContainSubstring("Removed 2 workload(s) from group"))
			Expect(string(output)).To(ContainSubstring(fmt.Sprintf("Group '%s' deleted successfully", groupName)))

			// Verify group workloads still exist (not deleted by default)
			stdout, _ := NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(groupWorkload1))
			Expect(stdout).To(ContainSubstring(groupWorkload2))

			// Verify non-group workloads are still running
			dockerCmd = exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", nonGroupWorkload1))
			output, err = dockerCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(nonGroupWorkload1))

			dockerCmd = exec.Command("docker", "ps", "--filter", fmt.Sprintf("name=%s", nonGroupWorkload2))
			output, err = dockerCmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(nonGroupWorkload2))

			// Clean up non-group workloads
			removeWorkload(nonGroupWorkload1)
			removeWorkload(nonGroupWorkload2)

			// Verify group is deleted
			stdout, _ = NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})

		It("should delete group and workloads with --with-workloads flag", func() {
			// Create a group
			groupName := fmt.Sprintf("with-workloads-group-%d", time.Now().UnixNano())
			createGroup(groupName)

			// Create workloads in the group
			workload1 := fmt.Sprintf("with-workloads-1-%d", time.Now().UnixNano())
			workload2 := fmt.Sprintf("with-workloads-2-%d", time.Now().UnixNano())

			createWorkloadInGroup(workload1, groupName)
			createWorkloadInGroup(workload2, groupName)

			// Verify workloads are running
			stdout, _ := NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(workload1))
			Expect(stdout).To(ContainSubstring(workload2))

			// Delete the group with --with-workloads flag (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "delete", "--with-workloads", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))
			Expect(string(output)).To(ContainSubstring("Deleted 2 workload(s) from group"))
			Expect(string(output)).To(ContainSubstring(fmt.Sprintf("Group '%s' deleted successfully", groupName)))

			// Verify workloads are deleted
			stdout, _ = NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(workload1))
			Expect(stdout).NotTo(ContainSubstring(workload2))

			// Verify group is deleted
			stdout, _ = NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})
	})
})

// Helper functions are defined in other test files
