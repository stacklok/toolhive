package e2e_test

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Group RM E2E Tests", func() {
	var (
		config           *e2e.TestConfig
		groupName        string
		secondGroupName  string
		createdWorkloads []string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		// Use a shared timestamp for all workload names in this test
		groupName = fmt.Sprintf("group-rm-cancel-group-%d", time.Now().UnixNano())
		secondGroupName = fmt.Sprintf("group-rm-cancel-group-2-%d", time.Now().UnixNano())
		createdWorkloads = []string{}

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		e2e.NewTHVCommand(config, "group", "create", groupName).ExpectSuccess()
		e2e.NewTHVCommand(config, "group", "create", secondGroupName).ExpectSuccess()
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up workloads first
			for _, workloadName := range createdWorkloads {
				err := e2e.StopAndRemoveMCPServer(config, workloadName)
				Expect(err).NotTo(HaveOccurred(), "Should be able to stop and remove server")
			}

			// Clean up groups
			err := e2e.RemoveGroup(config, groupName)
			Expect(err).NotTo(HaveOccurred(), "Should be able to remove group")
			err = e2e.RemoveGroup(config, secondGroupName)
			Expect(err).NotTo(HaveOccurred(), "Should be able to remove second group")
		}
	})

	createWorkloadInGroup := func(workloadName, groupName string) {
		e2e.NewTHVCommand(config, "run", "fetch", "--group", groupName, "--name", workloadName).ExpectSuccess()
		createdWorkloads = append(createdWorkloads, workloadName)
	}

	Describe("thv group rm command", func() {
		It("should return error when group does not exist", func() {
			groupName := fmt.Sprintf("group-rm-non-existent-group-%d", time.Now().UnixNano())
			_, stderr, err := e2e.NewTHVCommand(config, "group", "rm", groupName).ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("does not exist"))
		})

		It("should cancel deletion when user does not confirm", func() {
			// Add a workload to the group
			workloadName := fmt.Sprintf("group-rm-test-workload-%d", time.Now().UnixNano())
			createWorkloadInGroup(workloadName, groupName)

			// Verify the workload is running
			err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
			Expect(err).ToNot(HaveOccurred())

			// Try to delete the group but provide 'n' for no
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName)
			cmd.Stdin = strings.NewReader("n\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("Group deletion cancelled."))

			// Verify group still exists
			stdout, _ := e2e.NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(groupName))
		})

		It("should delete empty group successfully", func() {
			// Verify group exists
			stdout, _ := e2e.NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(groupName))

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring(fmt.Sprintf("Group '%s' deleted successfully", groupName)))

			// Verify group is deleted
			stdout, _ = e2e.NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})

		It("should delete group with workloads", func() {
			// Create workloads in the group
			groupWorkload1 := fmt.Sprintf("group-rm-group-workload-1-%d", GinkgoRandomSeed())
			groupWorkload2 := fmt.Sprintf("group-rm-group-workload-2-%d", GinkgoRandomSeed())

			// Create workloads not in the group
			nonGroupWorkload1 := fmt.Sprintf("group-rm-non-group-workload-1-%d", GinkgoRandomSeed())
			nonGroupWorkload2 := fmt.Sprintf("group-rm-non-group-workload-2-%d", GinkgoRandomSeed())

			createWorkloadInGroup(groupWorkload1, groupName)
			createWorkloadInGroup(groupWorkload2, groupName)
			createWorkloadInGroup(nonGroupWorkload1, secondGroupName)
			createWorkloadInGroup(nonGroupWorkload2, secondGroupName)

			// Verify all workloads are running
			for _, workloadName := range []string{groupWorkload1, groupWorkload2, nonGroupWorkload1, nonGroupWorkload2} {
				err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			}

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))
			Expect(string(output)).To(ContainSubstring(fmt.Sprintf("Group '%s' deleted successfully", groupName)))

			// Verify group workloads still exist (not deleted by default)
			stdout, _ := e2e.NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).To(ContainSubstring(groupWorkload1))
			Expect(stdout).To(ContainSubstring(groupWorkload2))

			// Verify non-group workloads are still running
			Expect(e2e.IsServerRunning(config, nonGroupWorkload1)).To(BeTrue(), "Non-group workload %s is not running", nonGroupWorkload1)
			Expect(e2e.IsServerRunning(config, nonGroupWorkload2)).To(BeTrue(), "Non-group workload %s is not running", nonGroupWorkload2)

			// Verify group is deleted
			stdout, _ = e2e.NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})

		It("should delete group and workloads with --with-workloads flag", func() {
			// Create multiple workloads in the group
			workload1 := fmt.Sprintf("group-rm-with-workloads-1-%d", GinkgoRandomSeed())
			workload2 := fmt.Sprintf("group-rm-with-workloads-2-%d", GinkgoRandomSeed())

			createWorkloadInGroup(workload1, groupName)
			createWorkloadInGroup(workload2, groupName)

			// Verify all workloads are running
			for _, workloadName := range []string{workload1, workload2} {
				err := e2e.WaitForMCPServer(config, workloadName, 60*time.Second)
				Expect(err).ToNot(HaveOccurred())
			}

			// Delete the group with --with-workloads flag (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName, "--with-workloads")
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))
			Expect(string(output)).To(ContainSubstring(fmt.Sprintf("Group '%s' deleted successfully", groupName)))

			// Verify workloads are deleted
			stdout, _ := e2e.NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(workload1))
			Expect(stdout).NotTo(ContainSubstring(workload2))

			// Verify group is deleted
			stdout, _ = e2e.NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})
	})
})
