// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

var _ = Describe("Read-only root filesystem", Label("core", "e2e"), func() {
	var (
		config     *e2e.TestConfig
		serverName string
	)

	BeforeEach(func() {
		config = e2e.NewTestConfig()
		serverName = e2e.GenerateUniqueServerName("readonly-rootfs-test")
		Expect(e2e.CheckTHVBinaryAvailable(config)).To(Succeed(), "thv binary should be available")
	})

	AfterEach(func() {
		if config.CleanupAfter {
			Expect(e2e.StopAndRemoveMCPServer(config, serverName)).To(Succeed(),
				"Should be able to stop and remove server")
		}
	})

	// inspect returns a Go-template field docker reports for the workload's container.
	inspect := func(format string) string {
		cmd := e2e.StartDockerCommand("inspect", "-f", format, serverName)
		out, err := cmd.CombinedOutput()
		ExpectWithOffset(1, err).ToNot(HaveOccurred(), "docker inspect failed: %s", out)
		return strings.TrimSpace(string(out))
	}

	DescribeTable("sets the container's root filesystem mode",
		func(extraArgs []string, wantReadOnly string, wantScratchTmpfs bool) {
			args := append([]string{"run", "--name", serverName}, extraArgs...)
			args = append(args, "fetch")
			e2e.NewTHVCommand(config, args...).ExpectSuccess()

			By("Waiting for the server to be running")
			Expect(e2e.WaitForMCPServer(config, serverName, 60*time.Second)).To(Succeed(),
				"Server should be running within 60 seconds")

			By("Inspecting the container's HostConfig")
			Expect(inspect("{{.HostConfig.ReadonlyRootfs}}")).To(Equal(wantReadOnly))

			tmpfs := inspect("{{json .HostConfig.Tmpfs}}")
			if wantScratchTmpfs {
				for _, path := range []string{"/tmp", "/var/tmp", "/run"} {
					Expect(tmpfs).To(ContainSubstring(`"`+path+`":"rw,nosuid,nodev,noexec,size=64m"`),
						"%s should get a capped scratch tmpfs", path)
				}
			} else {
				Expect(tmpfs).ToNot(ContainSubstring(`"/tmp"`), "no scratch tmpfs without --read-only-rootfs")
			}
		},
		Entry("read-only with scratch tmpfs when --read-only-rootfs is set", []string{"--read-only-rootfs"}, "true", true),
		Entry("writable by default", []string{}, "false", false),
	)
})
