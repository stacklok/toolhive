//go:build !linux

package docker

func calculateFinalPort(hostPort int, _ int, _ string) int {
	return hostPort
}
