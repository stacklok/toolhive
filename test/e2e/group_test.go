package e2e_test

// TODO: add back in once we have a working group command
/*import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Group", func() {
	var (
		config    *e2e.TestConfig
		groupName string
		thvBinary string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		groupName = "testgroup-e2e-" + fmt.Sprintf("%d", GinkgoRandomSeed())
		thvBinary = os.Getenv("THV_BINARY")
		if thvBinary == "" {
			Skip("THV_BINARY environment variable not set")
		}

		// Check if thv binary is available
		err := e2e.CheckTHVBinaryAvailable(config)
		Expect(err).ToNot(HaveOccurred(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			// Clean up the group if it exists
			cleanupManager, cleanupErr := groups.NewManager()
			if cleanupErr == nil {
				ctx := context.Background()
				_ = cleanupManager.Delete(ctx, groupName)
			}
		}
	})

	Describe("Creating groups", func() {
		Context("when creating a new group", func() {
			It("should successfully create the group", func() {
				By("Creating a group via CLI")
				cmd := exec.Command(thvBinary, "group", "create", groupName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to create group: %s", string(output))

				By("Verifying the group was created via manager")
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				exists, err := manager.Exists(ctx, groupName)
				Expect(err).ToNot(HaveOccurred())
				Expect(exists).To(BeTrue(), "Group should exist after creation")

				By("Verifying we can get the group")
				group, err := manager.Get(ctx, groupName)
				Expect(err).ToNot(HaveOccurred())
				Expect(group.Name).To(Equal(groupName))
			})
		})

		Context("when creating a duplicate group", func() {
			BeforeEach(func() {
				By("Creating the initial group")
				cmd := exec.Command(thvBinary, "group", "create", groupName)
				output, err := cmd.CombinedOutput()
				Expect(err).ToNot(HaveOccurred(), "Failed to create initial group: %s", string(output))
			})

			It("should fail when creating the same group again", func() {
				By("Attempting to create the same group again")
				cmd := exec.Command(thvBinary, "group", "create", groupName)
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail when creating duplicate group")
				Expect(string(output)).To(ContainSubstring("already exists"))
			})
		})

		Context("when providing invalid arguments", func() {
			It("should fail when no group name is provided", func() {
				By("Attempting to create a group without a name")
				cmd := exec.Command(thvBinary, "group", "create")
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail when no group name provided")
				Expect(string(output)).To(ContainSubstring("accepts 1 arg(s)"))
			})

			It("should fail with empty group name", func() {
				By("Attempting to create a group with empty name")
				cmd := exec.Command(thvBinary, "group", "create", "")
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Should fail with empty group name")
				Expect(string(output)).To(ContainSubstring("group name cannot be empty"))
			})
		})

		Context("when creating groups concurrently", func() {
			It("should handle concurrent creation gracefully", func() {
				By("Starting concurrent group creation")
				done := make(chan bool, 2)
				errors := make(chan error, 2)

				// First goroutine
				go func() {
					cmd := exec.Command(thvBinary, "group", "create", groupName)
					_, err := cmd.CombinedOutput()
					if err != nil {
						errors <- err
					}
					done <- true
				}()

				// Second goroutine (should fail)
				go func() {
					// Wait a bit to ensure the first one starts
					time.Sleep(100 * time.Millisecond)
					cmd := exec.Command(thvBinary, "group", "create", groupName)
					_, err := cmd.CombinedOutput()
					if err != nil {
						errors <- err
					}
					done <- true
				}()

				By("Waiting for both operations to complete")
				<-done
				<-done

				By("Verifying exactly one concurrent creation failed")
				errorCount := len(errors)
				Expect(errorCount).To(Equal(1), "Exactly one concurrent creation should fail")

				By("Verifying the group exists")
				manager, err := groups.NewManager()
				Expect(err).ToNot(HaveOccurred())

				ctx := context.Background()
				exists, err := manager.Exists(ctx, groupName)
				Expect(err).ToNot(HaveOccurred())
				Expect(exists).To(BeTrue(), "Group should exist after concurrent creation")
			})
		})
	})
})
*/
