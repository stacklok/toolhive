// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Group Remove E2E Tests", Label("core", "groups", "e2e"), func() {
	var (
		config           *e2e.TestConfig
		testGroupName    string
		secondGroupName  string
		createdWorkloads []string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		testGroupName = e2e.GenerateUniqueServerName("rm-test-group")
		secondGroupName = e2e.GenerateUniqueServerName("rm-test-group-2")
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
			groupName := e2e.GenerateUniqueServerName("rm-non-existent-group")
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
			groupWorkload1 := e2e.GenerateUniqueServerName("rm-group-workload-1")
			groupWorkload2 := e2e.GenerateUniqueServerName("rm-group-workload-2")
			nonGroupWorkload1 := e2e.GenerateUniqueServerName("rm-non-group-workload-1")
			nonGroupWorkload2 := e2e.GenerateUniqueServerName("rm-non-group-workload-2")
			createWorkloadInGroup(groupWorkload1, testGroupName)
			createWorkloadInGroup(groupWorkload2, testGroupName)
			createWorkloadInGroup(nonGroupWorkload1, secondGroupName)
			createWorkloadInGroup(nonGroupWorkload2, secondGroupName)

			// Wait for the workloads to appear in thv list
			e2e.ExpectMCPServersRunning(config, groupWorkload1, groupWorkload2, nonGroupWorkload1, nonGroupWorkload2)

			// Remove all workloads in the group
			e2e.NewTHVCommand(config, "rm", "--group", testGroupName).ExpectSuccess()

			// Verify only group workloads are deleted
			stdout, _ := e2e.NewTHVCommand(config, "list").ExpectSuccess()
			Expect(stdout).NotTo(ContainSubstring(groupWorkload1))
			Expect(stdout).NotTo(ContainSubstring(groupWorkload2))
			Expect(stdout).To(ContainSubstring(nonGroupWorkload1))
			Expect(stdout).To(ContainSubstring(nonGroupWorkload2))
		})

		It("should require group flag when no workload name provided", func() {
			_, stderr, err := e2e.NewTHVCommand(config, "rm").ExpectFailure()
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("at least one workload name must be provided"))
		})
	})
})
