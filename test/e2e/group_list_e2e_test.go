package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Group List E2E", func() {
	var thvBinary string

	BeforeEach(func() {
		thvBinary = os.Getenv("THV_BINARY")
		if thvBinary == "" {
			Skip("THV_BINARY environment variable not set")
		}
	})

	Describe("Basic Group List Functionality", func() {
		It("should show help for group list command", func() {
			By("Getting group list command help")
			cmd := exec.Command(thvBinary, "group", "list", "--help")
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Group list help should succeed")

			outputStr := string(output)
			Expect(outputStr).To(ContainSubstring("List all logical groups"), "Should show command description")
			Expect(outputStr).To(ContainSubstring("Usage:"), "Should show usage information")
		})

		It("should list existing groups", func() {
			By("Running group list command")
			cmd := exec.Command(thvBinary, "group", "list")
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Group list command should succeed")

			outputStr := string(output)
			Expect(outputStr).To(ContainSubstring("NAME"), "Should show table header")
			Expect(outputStr).To(Not(ContainSubstring("Found")), "Should not show old format")
			Expect(outputStr).To(Not(ContainSubstring("  - ")), "Should not show bullet point format")
		})

		It("should show groups in consistent format", func() {
			By("Running group list command and checking format")
			cmd := exec.Command(thvBinary, "group", "list")
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Group list command should succeed")

			outputStr := string(output)
			lines := strings.Split(strings.TrimSpace(outputStr), "\n")

			// First line should be the header
			Expect(lines[0]).To(Equal("NAME"), "First line should be table header")

			// Check that subsequent lines are group names (not empty and not bullet points)
			for i := 1; i < len(lines); i++ {
				line := strings.TrimSpace(lines[i])
				if line != "" {
					Expect(line).To(Not(MatchRegexp(`^\s*-\s*.*$`)), "Groups should not be formatted as bullet points")
					Expect(line).To(Not(BeEmpty()), "Group names should not be empty")
				}
			}
		})
	})

	Describe("Group Creation and Listing", func() {
		It("should create a new group and show it in the list", func() {
			// Create unique group name for this test
			testGroupName := "e2e-test-group-" + time.Now().Format("20060102150405") + "-" + fmt.Sprintf("%d", time.Now().UnixNano()%1000000)

			// Clean up the group after the test
			defer cleanupSpecificGroup(testGroupName)

			By("Creating a new test group")
			createCmd := exec.Command(thvBinary, "group", "create", testGroupName)
			createOutput, err := createCmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Group creation should succeed")
			Expect(string(createOutput)).To(ContainSubstring("created successfully"))

			By("Verifying the group appears in the sorted list")
			listCmd := exec.Command(thvBinary, "group", "list")
			listOutput, err := listCmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Group list should succeed")

			outputStr := string(listOutput)
			Expect(outputStr).To(ContainSubstring(testGroupName), "New group should appear in the sorted list")
		})

		It("should handle multiple group creation and listing", func() {
			// Create unique base name for this test
			baseGroupName := "e2e-test-group-" + time.Now().Format("20060102150405") + "-" + fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
			groupNames := []string{
				baseGroupName + "-1",
				baseGroupName + "-2",
				baseGroupName + "-3",
			}

			// Clean up all groups created by this test
			defer func() {
				for _, groupName := range groupNames {
					cleanupSpecificGroup(groupName)
				}
			}()

			By("Creating multiple test groups")
			for _, groupName := range groupNames {
				createCmd := exec.Command(thvBinary, "group", "create", groupName)
				createOutput, err := createCmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Group creation should succeed for %s", groupName)
				Expect(string(createOutput)).To(ContainSubstring("created successfully"))
			}

			By("Verifying all groups appear in the sorted list")
			listCmd := exec.Command(thvBinary, "group", "list")
			listOutput, err := listCmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Group list should succeed")

			outputStr := string(listOutput)
			for _, groupName := range groupNames {
				Expect(outputStr).To(ContainSubstring(groupName), "Group %s should appear in the sorted list", groupName)
			}
		})
	})

	Describe("Error Handling", func() {
		It("should handle invalid group list arguments", func() {
			By("Running group list with invalid arguments")
			cmd := exec.Command(thvBinary, "group", "list", "invalid-arg")
			output, err := cmd.CombinedOutput()
			Expect(err).To(HaveOccurred(), "Should fail with invalid arguments")
			Expect(string(output)).To(ContainSubstring("accepts 0 arg(s)"))
		})

		It("should handle group list with debug flag", func() {
			By("Running group list with debug flag")
			cmd := exec.Command(thvBinary, "group", "list", "--debug")
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Should succeed with debug flag")
			Expect(string(output)).To(ContainSubstring("NAME"))
		})
	})

	Describe("Integration with Group Commands", func() {
		It("should work with group create and list workflow", func() {
			// Create unique group name for this test
			testGroupName := "e2e-test-group-" + time.Now().Format("20060102150405") + "-" + fmt.Sprintf("%d", time.Now().UnixNano()%1000000)

			// Clean up the group after the test
			defer cleanupSpecificGroup(testGroupName)

			By("Creating a group and immediately listing it")
			createCmd := exec.Command(thvBinary, "group", "create", testGroupName)
			createOutput, err := createCmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Group creation should succeed")
			Expect(string(createOutput)).To(ContainSubstring("created successfully"))

			listCmd := exec.Command(thvBinary, "group", "list")
			listOutput, err := listCmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Group list should succeed")

			outputStr := string(listOutput)
			Expect(outputStr).To(ContainSubstring(testGroupName), "Newly created group should appear in list")
		})
	})

	Describe("Output Consistency", func() {
		It("should produce consistent output format", func() {
			By("Running group list command multiple times")
			outputs := make([]string, 3)
			for i := 0; i < 3; i++ {
				cmd := exec.Command(thvBinary, "group", "list")
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Group list should succeed on iteration %d", i+1)
				outputs[i] = string(output)
			}

			By("Verifying outputs are consistent")
			for i := 1; i < len(outputs); i++ {
				groups1 := extractGroupNames(outputs[i-1])
				groups2 := extractGroupNames(outputs[i])

				Expect(groups1).To(Equal(groups2), "Group lists should be consistent between runs")
			}
		})

		It("should display groups in alphanumeric order", func() {
			By("Running group list command")
			cmd := exec.Command(thvBinary, "group", "list")
			output, err := cmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Group list should succeed")

			outputStr := string(output)
			groups := extractGroupNames(outputStr)

			By("Verifying groups are sorted alphanumerically")
			Expect(len(groups)).To(BeNumerically(">", 0), "Should have at least one group to test sorting")

			// Check that groups are in ascending alphanumeric order
			for i := 1; i < len(groups); i++ {
				Expect(strings.Compare(groups[i-1], groups[i])).To(BeNumerically("<=", 0),
					"Group '%s' should come before or equal to '%s' in alphanumeric order",
					groups[i-1], groups[i])
			}
		})

		It("should handle mixed alphanumeric group names correctly", func() {
			// Create unique base name for this test
			baseGroupName := "e2e-test-group-" + time.Now().Format("20060102150405") + "-" + fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
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

			// Create full group names
			fullGroupNames := make([]string, len(mixedGroupNames))
			for i, mixedName := range mixedGroupNames {
				fullGroupNames[i] = baseGroupName + "-" + mixedName
			}

			// Clean up all groups created by this test
			defer func() {
				for _, groupName := range fullGroupNames {
					cleanupSpecificGroup(groupName)
				}
			}()

			By("Creating test groups with mixed alphanumeric names")
			// Create groups with mixed names
			for _, groupName := range fullGroupNames {
				createCmd := exec.Command(thvBinary, "group", "create", groupName)
				createOutput, err := createCmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Group creation should succeed for %s", groupName)
				Expect(string(createOutput)).To(ContainSubstring("created successfully"))
			}

			By("Verifying groups are sorted correctly")
			listCmd := exec.Command(thvBinary, "group", "list")
			listOutput, err := listCmd.CombinedOutput()
			Expect(err).ToNot(HaveOccurred(), "Group list should succeed")

			outputStr := string(listOutput)
			groups := extractGroupNames(outputStr)

			// Find our test groups in the output
			var testGroups []string
			for _, group := range groups {
				for _, fullName := range fullGroupNames {
					if group == fullName {
						testGroups = append(testGroups, group)
						break
					}
				}
			}

			By("Verifying test groups are in alphanumeric order")
			Expect(len(testGroups)).To(Equal(len(fullGroupNames)), "All test groups should be found")

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
