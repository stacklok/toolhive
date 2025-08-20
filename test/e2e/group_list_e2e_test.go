package e2e_test

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Group List E2E", func() {
	var testGroupName string
	var config *e2e.TestConfig
	var createdGroups []string

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		createdGroups = []string{}

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")

		// Generate unique test group name with timestamp and nanoseconds
		testGroupName = "e2e-test-group-" + time.Now().Format("20060102150405") + "-" + fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up all created groups
			for _, groupName := range createdGroups {
				err := e2e.RemoveGroup(config, groupName)
				Expect(err).ToNot(HaveOccurred(), "Should be able to remove created group %s after tests", groupName)
			}
		}
	})

	Describe("Group Creation and Listing", func() {
		It("should create a new group and show it in the list", func() {
			By("Creating a new test group")
			e2e.CreateAndTrackGroup(config, testGroupName, &createdGroups)

			By("Verifying the group appears in the sorted list")
			outputStr, _ := e2e.NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(outputStr).To(ContainSubstring(testGroupName), "New group should appear in the sorted list")
		})

		It("should handle multiple group creation and listing", func() {
			By("Creating multiple test groups")
			groupNames := []string{
				testGroupName + "-1",
				testGroupName + "-2",
				testGroupName + "-3",
			}

			for _, groupName := range groupNames {
				e2e.CreateAndTrackGroup(config, groupName, &createdGroups)
			}

			By("Verifying all groups appear in the sorted list")
			outputStr, _ := e2e.NewTHVCommand(config, "group", "list").ExpectSuccess()
			for _, groupName := range groupNames {
				Expect(outputStr).To(ContainSubstring(groupName), "Group %s should appear in the sorted list", groupName)
			}
		})
	})

	Describe("Integration with Group Commands", func() {
		It("should work with group create and list workflow", func() {
			By("Creating a group")
			e2e.CreateAndTrackGroup(config, testGroupName, &createdGroups)

			By("Listing groups immediately after creation")
			outputStr, _ := e2e.NewTHVCommand(config, "group", "list").ExpectSuccess()
			Expect(outputStr).To(ContainSubstring(testGroupName), "New group should appear in the list")

			By("Verifying group count increases")
			lines := strings.Split(strings.TrimSpace(outputStr), "\n")
			Expect(lines[0]).To(Equal("NAME"), "Should show table header")
		})
	})

	Describe("Output Consistency", func() {
		It("should display groups in alphanumeric order", func() {
			By("Creating test groups with mixed alphanumeric names")
			mixedGroupNames := []string{
				"group-123",
				"group-abc",
				"group1",
				"group2",
				"group_alpha",
				"group_beta",
				"testgroup",
				"testgroup1",
				"testgroup2",
			}

			// Create groups with mixed names
			for _, groupName := range mixedGroupNames {
				e2e.CreateAndTrackGroup(config, testGroupName+"-"+groupName, &createdGroups)
			}

			By("Verifying groups are sorted correctly")
			outputStr, _ := e2e.NewTHVCommand(config, "group", "list").ExpectSuccess()
			groups := extractGroupNames(outputStr)

			// Find our test groups in the output
			var testGroups []string
			for _, group := range groups {
				for _, mixedName := range mixedGroupNames {
					if strings.Contains(group, testGroupName+"-"+mixedName) {
						testGroups = append(testGroups, group)
						break
					}
				}
			}

			By("Verifying test groups are in alphanumeric order")
			Expect(len(testGroups)).To(Equal(len(mixedGroupNames)), "All test groups should be found")

			// Check that our test groups are sorted correctly
			for i := 1; i < len(testGroups); i++ {
				Expect(strings.Compare(testGroups[i-1], testGroups[i])).To(BeNumerically("<=", 0),
					"Test group '%s' should come before or equal to '%s' in alphanumeric order",
					testGroups[i-1], testGroups[i])
			}
		})
	})
})

// Helper function to extract group names from list output
func extractGroupNames(output string) []string {
	var groups []string
	lines := strings.Split(strings.TrimSpace(output), "\n")

	// Skip the first line (header line)
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line != "" && line != "NAME" {
			groups = append(groups, line)
		}
	}

	return groups
}
