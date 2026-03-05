//go:build !windows

package networking

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// GetProcessOnPort returns the PID of the process listening on the given TCP port.
// Returns 0 if the port is free or if the holder cannot be determined.
// Uses lsof which is available on Linux and macOS.
func GetProcessOnPort(port int) (int, error) {
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port %d", port)
	}

	// Listener-only:
	// -nP: no DNS/service-name resolution
	// -t: PIDs only
	// -iTCP:PORT: restrict to TCP on PORT
	// -sTCP:LISTEN: only listeners that are blocking the bind
	// #nosec G204 -- port validated above
	cmd := exec.Command("lsof", "-nP", "-t", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return 0, nil // no listeners
		}
		return 0, fmt.Errorf("lsof failed: %w", err)
	}

	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	if len(pids) == 0 {
		return 0, nil
	}

	// Deterministic: choose smallest PID (or change policy)
	sort.Ints(pids)
	return pids[0], nil
}
