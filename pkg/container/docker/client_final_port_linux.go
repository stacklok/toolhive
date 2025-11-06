//go:build linux

package docker

func calculateFinalPort(hostPort int, firstPortInt int, networkName string) int {
	if networkName == "host" {
		return firstPortInt
	}
	return hostPort
}
