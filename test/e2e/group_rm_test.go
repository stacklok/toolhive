// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Group RM E2E Tests", Label("core", "groups", "e2e"), func() {
	var (
		config           *e2e.TestConfig
		groupName        string
		secondGroupName  string
		createdWorkloads []string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		groupName = e2e.GenerateUniqueServerName("group-rm-cancel-group")
		secondGroupName = e2e.GenerateUniqueServerName("group-rm-cancel-group-2")
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
			groupName := e2e.GenerateUniqueServerName("group-rm-non-existent-group")
			_, stderr, err := e2e.NewTHVCommand(config, "group", "rm", groupName).ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("does not exist"))
		})

		It("should cancel deletion when user does not confirm", func() {
			// Add a workload to the group
			workloadName := e2e.GenerateUniqueServerName("group-rm-test-workload")
			createWorkloadInGroup(workloadName, groupName)

			// Verify the workload is running
			e2e.ExpectMCPServersRunning(config, workloadName)

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
			_, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())

			// Verify group is deleted
			stdout, _ = e2e.NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupName))
		})

		It("should delete group with workloads", func() {
			// Two members and two non-members are what make "rm keeps every
			// member and leaves every non-member running" an assertion rather
			// than an anecdote, so the setup stays at four. The workload count
			// is not what makes this spec flaky: the same readiness wait has
			// timed out in the two-workload specs here and in
			// list_group_e2e_test.go (#6040).
			groupWorkload1 := e2e.GenerateUniqueServerName("group-rm-group-workload-1")
			groupWorkload2 := e2e.GenerateUniqueServerName("group-rm-group-workload-2")

			// Create workloads not in the group
			nonGroupWorkload1 := e2e.GenerateUniqueServerName("group-rm-non-group-workload-1")
			nonGroupWorkload2 := e2e.GenerateUniqueServerName("group-rm-non-group-workload-2")

			createWorkloadInGroup(groupWorkload1, groupName)
			createWorkloadInGroup(groupWorkload2, groupName)
			createWorkloadInGroup(nonGroupWorkload1, secondGroupName)
			createWorkloadInGroup(nonGroupWorkload2, secondGroupName)

			// Verify all workloads are running
			e2e.ExpectMCPServersRunning(config, groupWorkload1, groupWorkload2, nonGroupWorkload1, nonGroupWorkload2)

			// Delete the group (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName)
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))

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
			workload1 := e2e.GenerateUniqueServerName("group-rm-with-workloads-1")
			workload2 := e2e.GenerateUniqueServerName("group-rm-with-workloads-2")

			createWorkloadInGroup(workload1, groupName)
			createWorkloadInGroup(workload2, groupName)

			// Verify all workloads are running
			e2e.ExpectMCPServersRunning(config, workload1, workload2)

			// Delete the group with --with-workloads flag (provide confirmation)
			cmd := exec.Command(config.THVBinary, "group", "rm", groupName, "--with-workloads")
			cmd.Stdin = strings.NewReader("y\n")
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("WARNING:"))

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
